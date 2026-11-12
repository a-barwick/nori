variable "gateway_image" {
  description = "Immutable client gateway image. Empty leaves the public gateway disabled."
  type        = string
  default     = ""
  validation {
    condition     = var.gateway_image == "" || can(regex("@sha256:[a-f0-9]{64}$", var.gateway_image))
    error_message = "Use an image pinned by sha256 digest."
  }
}

locals {
  gateway_enabled = var.gateway_image != ""
  gateway_secret  = lookup(var.caller_key_secrets, "NORI_GATEWAY_CALLER_KEY", "")
}

resource "google_service_account" "gateway" {
  count        = local.gateway_enabled ? 1 : 0
  account_id   = "${local.prefix}-gateway"
  display_name = "Nori client gateway"
}

resource "google_vertex_ai_reasoning_engine_iam_member" "gateway_runtime_query" {
  count            = local.gateway_enabled ? 1 : 0
  project          = local.project
  region           = local.region
  reasoning_engine = google_vertex_ai_reasoning_engine.runtime.name
  role             = google_project_iam_custom_role.query.name
  member           = "serviceAccount:${google_service_account.gateway[0].email}"
}

resource "google_secret_manager_secret_iam_member" "gateway_key" {
  count     = local.gateway_enabled && local.gateway_secret != "" ? 1 : 0
  secret_id = "projects/${local.project}/secrets/${local.gateway_secret}"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.gateway[0].email}"
}

resource "google_cloud_run_v2_service" "gateway" {
  count                = local.gateway_enabled ? 1 : 0
  name                 = "${local.prefix}-gateway"
  location             = local.region
  ingress              = "INGRESS_TRAFFIC_ALL"
  invoker_iam_disabled = true
  deletion_protection  = true

  # Each active instance holds the token buckets in memory.
  scaling {
    max_instance_count = 1
  }
  template {
    service_account                  = google_service_account.gateway[0].email
    timeout                          = "180s"
    max_instance_request_concurrency = 40
    containers {
      image = var.gateway_image
      resources {
        limits = { cpu = "1", memory = "512Mi" }
      }
      env {
        name  = "NORI_GATEWAY_UPSTREAM_URL"
        value = local.runtime_url
      }
      env {
        name = "NORI_GATEWAY_CALLER_KEY"
        value_source {
          secret_key_ref {
            secret  = local.gateway_secret
            version = "latest"
          }
        }
      }
    }
  }

  lifecycle {
    precondition {
      condition     = local.gateway_secret != ""
      error_message = "Configure NORI_GATEWAY_CALLER_KEY in caller_key_secrets."
    }
  }
  depends_on = [google_secret_manager_secret_iam_member.gateway_key, google_vertex_ai_reasoning_engine_iam_member.gateway_runtime_query]
}

resource "google_firestore_field" "client_authorization_expiry" {
  count      = local.gateway_enabled ? 1 : 0
  project    = local.project
  database   = google_firestore_database.state.name
  collection = "client_authorizations"
  field      = "expires_at"
  ttl_config {}
}

output "gateway_url" {
  value = try(google_cloud_run_v2_service.gateway[0].uri, null)
}

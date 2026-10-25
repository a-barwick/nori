locals {
  project = "bot-111549"
  region  = "us-west1"
  runner  = "nori-poc-runtime@bot-111549.iam.gserviceaccount.com"
  expires = var.session == null ? "1970-01-01T00:00:00Z" : var.session.access_expires_at
  # Copied from this Temporal account's Access template, not generic Temporal identities.
  temporal_impersonators = toset([
    "serverless-qtjow@prod-vs4padk9dkczcey6d6st9c58e.iam.gserviceaccount.com",
    "serverless-qtjow@prod-sbe56h6wr4sykaw47a6yg4h66.iam.gserviceaccount.com",
    "serverless-qtjow@prod-97gr6tp6zkv2t4epubffpz7kt.iam.gserviceaccount.com",
    "serverless-qtjow@prod-c4cqaqgbfzy23vpzkbxzrx378.iam.gserviceaccount.com",
    "serverless-qtjow@prod-79z2c6d76au6g5fcq768buqf9.iam.gserviceaccount.com",
  ])
  worker_env = {
    NORI_PROJECT_ID          = local.project
    FIRESTORE_DATABASE_ID    = "nori-poc"
    ARTIFACT_BUCKET          = "bot-111549-nori-poc"
    COMPUTER_ZONE            = "us-central1-a"
    COMPUTER_INSTANCE        = "nori-computer"
    MODEL_LOCATION           = "global"
    MODEL_NAME               = "gemini-3.1-flash-lite"
    TEMPORAL_ADDRESS         = "quickstart-nori.qtjow.tmprl.cloud:7233"
    TEMPORAL_NAMESPACE       = "quickstart-nori.qtjow"
    TEMPORAL_TASK_QUEUE      = "nori-tasks"
    TEMPORAL_DEPLOYMENT_NAME = "nori-task"
    TEMPORAL_CLOUD_RUN_ID    = "true"
  }
}

resource "google_project_service" "api" {
  for_each           = toset(["secretmanager.googleapis.com", "cloudscheduler.googleapis.com"])
  project            = local.project
  service            = each.value
  disable_on_destroy = false
}

# Only the empty secret container is Terraform-managed. Upload a version through
# native Secret Manager API from the root .env without logging the payload.
resource "google_secret_manager_secret" "temporal_api_key" {
  secret_id = "nori-temporal-api-key"
  replication {
    auto {}
  }
  lifecycle {
    prevent_destroy = true
  }
  depends_on = [google_project_service.api]
}

resource "google_secret_manager_secret_iam_member" "runner_key" {
  secret_id = google_secret_manager_secret.temporal_api_key.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${local.runner}"
}

resource "google_cloud_run_v2_worker_pool" "worker" {
  for_each            = var.worker_images
  name                = "nori-task-${each.key}"
  location            = local.region
  deletion_protection = true
  # MANUAL mode uses manual_instance_count. Temporal version configuration
  # supplies WCI max=1; native min/max do not constrain the manual count.
  scaling {
    scaling_mode          = "MANUAL"
    manual_instance_count = 0
  }
  template {
    service_account = local.runner
    containers {
      image = each.value
      resources {
        limits = { cpu = "1", memory = "512Mi" }
      }
      dynamic "env" {
        for_each = merge(local.worker_env, { TEMPORAL_BUILD_ID = each.key })
        content {
          name  = env.key
          value = env.value
        }
      }
      env {
        name = "TEMPORAL_API_KEY"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.temporal_api_key.secret_id
            version = "latest"
          }
        }
      }
    }
  }
  lifecycle {
    prevent_destroy = true
    # WCI and the shutdown job own count after initial creation. Terraform never
    # restarts paid capacity just because it refreshes an active pool.
    ignore_changes = [scaling[0].manual_instance_count]
  }
  depends_on = [google_secret_manager_secret_iam_member.runner_key]
}

resource "google_service_account" "invoker" {
  account_id   = "nori-temporal-invoker"
  display_name = "Temporal WCI scaler (not worker runtime)"
  disabled     = !var.wci_enabled
  lifecycle {
    precondition {
      condition     = !var.wci_enabled || (var.shutdown_verified && var.session != null && contains(keys(var.worker_images), (var.active_build_id == null ? "" : var.active_build_id)))
      error_message = "WCI requires a live-proven shutdown path, an explicit session, and exactly one retained active build."
    }
  }
}

resource "google_project_iam_custom_role" "pool_scaler" {
  role_id     = "noriTemporalPoolScaler"
  title       = "Nori Temporal pool read and scale"
  permissions = ["run.workerpools.get", "run.workerpools.update"]
}

# Resource IAM, not project run.developer. Grant expiry is an independent
# fence, including after an operator re-enables a previously disabled SA.
resource "google_cloud_run_v2_worker_pool_iam_member" "wci" {
  for_each = var.wci_enabled ? { for build, pool in google_cloud_run_v2_worker_pool.worker : build => pool if build == var.active_build_id } : {}
  project  = local.project
  location = local.region
  name     = each.value.name
  role     = google_project_iam_custom_role.pool_scaler.name
  member   = "serviceAccount:${google_service_account.invoker.email}"
  condition {
    title      = "nori-active-session"
    expression = "request.time < timestamp('${local.expires}')"
  }
}

resource "google_service_account_iam_member" "wci_runner" {
  service_account_id = "projects/${local.project}/serviceAccounts/${local.runner}"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.invoker.email}"
  condition {
    title      = "nori-active-session"
    expression = "request.time < timestamp('${local.expires}')"
  }
}

resource "google_service_account_iam_member" "temporal_impersonation" {
  for_each           = local.temporal_impersonators
  service_account_id = google_service_account.invoker.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${each.value}"
  condition {
    title      = "nori-active-session"
    expression = "request.time < timestamp('${local.expires}')"
  }
}

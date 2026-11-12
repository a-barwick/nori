variable "route_auth_mode" {
  type    = string
  default = "off"
  validation {
    condition     = contains(["off", "observe", "enforce"], var.route_auth_mode)
    error_message = "Choose off, observe, or enforce."
  }
}

variable "caller_key_secrets" {
  description = "Existing Secret Manager secret IDs. Store the values in Secret Manager, not Terraform variables."
  type        = map(string)
  default     = {}
  validation {
    condition = alltrue([for key, value in var.caller_key_secrets :
      contains(["NORI_WEB_CALLER_KEY", "NORI_INTERNAL_CALLER_KEY", "NORI_GATEWAY_CALLER_KEY", "NORI_QUEUE_CALLER_KEY"], key) && can(regex("^[A-Za-z0-9_-]+$", value))
    ])
    error_message = "Use Core caller key variable names and Secret Manager secret IDs."
  }
}

locals {
  web_caller_secrets = { for key, value in var.caller_key_secrets : key => value if key == "NORI_WEB_CALLER_KEY" }
}

resource "google_secret_manager_secret_iam_member" "caller_key_runtime" {
  for_each  = var.caller_key_secrets
  secret_id = "projects/${local.project}/secrets/${each.value}"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.runtime.email}"
}

resource "google_secret_manager_secret_iam_member" "caller_key_platform" {
  for_each  = var.caller_key_secrets
  secret_id = "projects/${local.project}/secrets/${each.value}"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:service-${local.number}@gcp-sa-aiplatform.iam.gserviceaccount.com"
}

resource "google_secret_manager_secret_iam_member" "caller_key_web" {
  for_each  = local.web_caller_secrets
  secret_id = "projects/${local.project}/secrets/${each.value}"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.web.email}"
}

output "route_auth_mode" {
  value = var.route_auth_mode
}

output "caller_key_secrets" {
  value = var.caller_key_secrets
}

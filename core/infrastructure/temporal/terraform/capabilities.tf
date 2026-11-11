locals {
  capability_secrets = {
    BRAVE_SEARCH_API_KEY          = "nori-brave-search-api-key"
    GOOGLE_CALENDAR_CLIENT_ID     = "nori-google-calendar-client-id"
    GOOGLE_CALENDAR_CLIENT_SECRET = "nori-google-calendar-client-secret"
  }
  retained_capability_secrets = merge(local.capability_secrets, {
    GOOGLE_CALENDAR_REFRESH_TOKEN = "nori-google-calendar-refresh-token"
    GOOGLE_CALENDAR_ACCOUNT       = "nori-google-calendar-account"
  })
}

# Terraform owns only these containers and references, never credential payloads.
resource "google_secret_manager_secret" "capability" {
  for_each  = local.production ? local.retained_capability_secrets : {}
  secret_id = each.value
  replication {
    auto {}
  }
  lifecycle {
    prevent_destroy = true
  }
  depends_on = [google_project_service.api]
}

resource "google_secret_manager_secret_iam_member" "capability_runner" {
  for_each  = google_secret_manager_secret.capability
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${local.runner}"
}

variable "oauth_redirect_url" {
  description = "OAuth callback outside production (production uses the custom web domain); workers need it to refresh account tokens."
  type        = string
  default     = ""
}

locals {
  # The key ring and key are owned by the application state (app/terraform/connected-accounts.tf), in us-central1.
  credential_kms_key = "projects/${local.project}/locations/us-central1/keyRings/${local.production ? "nori-credentials" : "nori-sandbox-credentials"}/cryptoKeys/connected-accounts"
  oauth_redirect_url = local.production ? "https://noribot.chat/api/v1/connections/oauth/callback" : var.oauth_redirect_url
  connected_accounts_env = merge({
    NORI_CREDENTIAL_STORE   = "database"
    NORI_CREDENTIAL_KMS_KEY = local.credential_kms_key
    NORI_USER_SENDS         = "ask"
    NORI_MAIL_RECIPIENTS    = "self"
  }, local.oauth_redirect_url == "" ? {} : { NORI_OAUTH_REDIRECT_URL = local.oauth_redirect_url })
}

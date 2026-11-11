# Connected accounts for the first slice (Google sign-in from Apps or a task).
#
# Credential custody: NORI_CREDENTIAL_STORE=database keeps each account's tokens as envelope-encrypted rows in the
# user's own Firestore store; data keys are wrapped by the Cloud KMS key below. This is the coordinator's choice under
# Austin's "get it ready" for the PR, not a recorded D1 decision; the mechanism stays configurable.
#
# The runtime service account runs both the API runtime and the request workers (temporal state, local.runner), so it
# is the only identity granted the key, and only to encrypt and decrypt.

variable "oauth_redirect_url" {
  description = "OAuth callback for connected accounts outside production (production uses the custom web domain). Empty leaves sign-in unconfigured, as before."
  type        = string
  default     = ""
}

variable "push_delivery" {
  description = "Wire Web Push (VAPID) secrets into the runtime. Leave false until both nori*-vapid-* secrets hold a version, or the runtime deployment fails to resolve them."
  type        = bool
  default     = false
}

variable "vapid_subject" {
  description = "VAPID subject (a mailto: or https: contact URL), sent to push services with each push."
  type        = string
  default     = "https://noribot.chat"
}

locals {
  credential_key_ring = local.production ? "nori-credentials" : "nori-sandbox-credentials"
  # Matches the temporal state's local of the same name; both states pass it to their services.
  credential_kms_key = "projects/${local.project}/locations/${local.region}/keyRings/${local.credential_key_ring}/cryptoKeys/connected-accounts"
  # The web proxies /api/v1/connections/** to the runtime; Google redirects the browser here.
  oauth_redirect_url = local.production ? "https://${google_cloud_run_domain_mapping.web[0].name}/api/v1/connections/oauth/callback" : var.oauth_redirect_url
  # Client registered in Google Cloud Console; payloads live only in Secret Manager (containers: temporal state).
  google_client_secrets = local.production ? {
    GOOGLE_CALENDAR_CLIENT_ID     = "nori-google-calendar-client-id"
    GOOGLE_CALENDAR_CLIENT_SECRET = "nori-google-calendar-client-secret"
  } : {}
  vapid_secrets = {
    NORI_VAPID_PUBLIC_KEY  = local.production ? "nori-vapid-public-key" : "nori-sandbox-vapid-public-key"
    NORI_VAPID_PRIVATE_KEY = local.production ? "nori-vapid-private-key" : "nori-sandbox-vapid-private-key"
  }
  connected_accounts_env = merge({
    NORI_CREDENTIAL_STORE   = "database"
    NORI_CREDENTIAL_KMS_KEY = local.credential_kms_key
    # Safe production defaults for the slice: a send the user asked for still asks for approval, and Gmail sends only
    # to the connected account's own address. Both are interim settings, reversible without a release of code.
    NORI_USER_SENDS      = "ask"
    NORI_MAIL_RECIPIENTS = "self"
    }, local.oauth_redirect_url == "" ? {} : { NORI_OAUTH_REDIRECT_URL = local.oauth_redirect_url },
  var.push_delivery ? { NORI_VAPID_SUBJECT = var.vapid_subject } : {})
  connected_accounts_secret_env = merge(local.google_client_secrets, var.push_delivery ? local.vapid_secrets : {})
}

resource "google_project_service" "kms" {
  service            = "cloudkms.googleapis.com"
  disable_on_destroy = false
}

resource "google_kms_key_ring" "credentials" {
  name     = local.credential_key_ring
  location = local.region
  lifecycle {
    prevent_destroy = true
  }
  depends_on = [google_project_service.kms]
}

resource "google_kms_crypto_key" "connected_accounts" {
  name            = "connected-accounts"
  key_ring        = google_kms_key_ring.credentials.id
  purpose         = "ENCRYPT_DECRYPT"
  rotation_period = "7776000s" # 90 days; earlier versions stay enabled, so older wrapped keys still unwrap.
  lifecycle {
    prevent_destroy = true
  }
}

# Encrypt and decrypt only: no key administration, no access to other keys.
resource "google_kms_crypto_key_iam_member" "runtime_credentials" {
  crypto_key_id = google_kms_crypto_key.connected_accounts.id
  role          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  member        = "serviceAccount:${google_service_account.runtime.email}"
}

# The runtime identity already reads the Google client secrets (temporal state, capability_runner). Agent Platform
# resolves secret environment variables at deployment, so its service agent needs read access too.
resource "google_secret_manager_secret_iam_member" "google_client_platform" {
  for_each  = local.google_client_secrets
  secret_id = each.value
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:service-${local.number}@gcp-sa-aiplatform.iam.gserviceaccount.com"
}

# Containers only. Terraform never holds the key pair: an operator adds the versions (see backend-status.md).
resource "google_secret_manager_secret" "vapid" {
  for_each  = local.vapid_secrets
  secret_id = each.value
  replication {
    auto {}
  }
  lifecycle {
    prevent_destroy = true
  }
}

resource "google_secret_manager_secret_iam_member" "vapid_runtime" {
  for_each  = google_secret_manager_secret.vapid
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.runtime.email}"
}

resource "google_secret_manager_secret_iam_member" "vapid_platform" {
  for_each  = local.production ? google_secret_manager_secret.vapid : {}
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:service-${local.number}@gcp-sa-aiplatform.iam.gserviceaccount.com"
}

output "credential_kms_key" {
  value = google_kms_crypto_key.connected_accounts.id
}

output "oauth_redirect_url" {
  value = local.oauth_redirect_url
}

# Secret Manager secrets. Terraform owns each secret and who can read it.
# Secret values stay out of Terraform: add a version with
#   gcloud secrets versions add <name> --data-file=-
# and, when a service pins a numeric version, update the pin in run.tf.

locals {
  secrets = toset([
    "nori-apns-private-key",
    "nori-credential-key",
    "nori-exa-api-key",
    "nori-google-workspace-beta-client-secret",
    "nori-home-ai-db-url",
    "nori-home-ai-key",
    "nori-jev-api-key",
    "nori-mac-google-identities",
    "nori-mac-pilot-identities",
    "nori-postgres-url",
    "nori-private-beta-emails",
    "nori-sentry-auth-token",
    "nori-service-token-key",
    "nori-sprites-api-key",
    "nori-terraform-private",
    "nori-vercel-ai-gateway-key",
  ])

  # nori-runtime and nori-web also read every secret through their project-level
  # secretAccessor role in iam.tf.
  secret_readers = {
    "apns-key-runtime"          = { secret = "nori-apns-private-key", member = google_service_account.nori_runtime.member }
    "exa-runtime"               = { secret = "nori-exa-api-key", member = google_service_account.nori_runtime.member }
    "home-ai-key-runtime"       = { secret = "nori-home-ai-key", member = google_service_account.nori_runtime.member }
    "sprites-runtime"           = { secret = "nori-sprites-api-key", member = google_service_account.nori_runtime.member }
    "vercel-ai-gateway-runtime" = { secret = "nori-vercel-ai-gateway-key", member = google_service_account.nori_runtime.member }
    "mac-identities-web-mac"    = { secret = "nori-mac-google-identities", member = google_service_account.nori_web_mac.member }
    "mac-pilot-web-mac"         = { secret = "nori-mac-pilot-identities", member = google_service_account.nori_web_mac.member }
    "sentry-auth-ci-images"     = { secret = "nori-sentry-auth-token", member = google_service_account.nori_ci_images.member }
    "service-token-web-mac"     = { secret = "nori-service-token-key", member = google_service_account.nori_web_mac.member }
  }
}

resource "google_secret_manager_secret" "this" {
  for_each = local.secrets

  secret_id = each.key

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_iam_member" "readers" {
  for_each = local.secret_readers

  secret_id = google_secret_manager_secret.this[each.value.secret].id
  role      = "roles/secretmanager.secretAccessor"
  member    = each.value.member
}

# Core can mount the roster before the first private tester sync.
resource "google_secret_manager_secret_version" "empty_private_beta" {
  secret      = google_secret_manager_secret.this["nori-private-beta-emails"].id
  secret_data = "[]"
}

# GitHub supplies the production gateway key to Terraform during plan and apply.
# The write-only argument keeps the key out of saved plans and state.
variable "platform_ai_gateway_key" {
  type      = string
  sensitive = true
  ephemeral = true
  nullable  = false
  validation {
    condition     = startswith(var.platform_ai_gateway_key, "vck_")
    error_message = "Supply the production Vercel AI Gateway credential."
  }
}

resource "google_secret_manager_secret_version" "platform_gateway" {
  secret                 = google_secret_manager_secret.this["nori-vercel-ai-gateway-key"].id
  secret_data_wo         = var.platform_ai_gateway_key
  secret_data_wo_version = 1
  lifecycle {
    create_before_destroy = true
  }
}

# JSON object with values that must not appear in this public repository:
#   allowed_emails                     NORI_ALLOWED_EMAILS on Core, Worker and migrations
#   google_workspace_test_users        NORI_GOOGLE_WORKSPACE_TEST_USERS on Core and Worker ("" removes it)
#   apns_key_id                        NORI_APNS_KEY_ID on Core (optional; "" disables APNs)
#   apns_team_id                       NORI_APNS_TEAM_ID on Core (optional; "" disables APNs)
#   billing_account                    Optional GCP billing account ID ("" disables billing budget alert)
#   billing_monthly_budget_usd         Optional monthly budget amount in USD (default 150)
data "google_secret_manager_secret_version" "terraform_private" {
  secret = "nori-terraform-private"
}

# Secret Manager secrets. Terraform owns each secret and who can read it.
# Secret values stay out of Terraform: add a version with
#   gcloud secrets versions add <name> --data-file=-
# and, when a service pins a numeric version, update the pin in run.tf.

locals {
  secrets = toset([
    "nori-credential-key",
    "nori-exa-api-key",
    "nori-google-workspace-beta-client-secret",
    "nori-home-ai-db-url",
    "nori-home-ai-key",
    "nori-jev-api-key",
    "nori-mac-google-identities",
    "nori-mac-pilot-identities",
    "nori-postgres-url",
    "nori-sentry-auth-token",
    "nori-service-token-key",
    "nori-sprites-api-key",
    "nori-terraform-private",
  ])

  # nori-runtime and nori-web also read every secret through their project-level
  # secretAccessor role in iam.tf.
  secret_readers = {
    "exa-runtime"            = { secret = "nori-exa-api-key", member = google_service_account.nori_runtime.member }
    "home-ai-key-runtime"    = { secret = "nori-home-ai-key", member = google_service_account.nori_runtime.member }
    "sprites-runtime"        = { secret = "nori-sprites-api-key", member = google_service_account.nori_runtime.member }
    "mac-identities-web-mac" = { secret = "nori-mac-google-identities", member = google_service_account.nori_web_mac.member }
    "mac-pilot-web-mac"      = { secret = "nori-mac-pilot-identities", member = google_service_account.nori_web_mac.member }
    "service-token-web-mac"  = { secret = "nori-service-token-key", member = google_service_account.nori_web_mac.member }
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

# JSON object with values that must not appear in this public repository:
#   allowed_emails               NORI_ALLOWED_EMAILS on Core, Worker and migrations
#   google_workspace_test_users  NORI_GOOGLE_WORKSPACE_TEST_USERS on Core and Worker ("" removes it)
data "google_secret_manager_secret_version" "terraform_private" {
  secret = "nori-terraform-private"
}

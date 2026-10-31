# Empty secret containers and scoped grants only. Credential payloads never
# enter Terraform variables/state. Setup uploads the CI-only management key;
# each logical run writes its bounded OpenRouter key as a native secret version.
resource "google_secret_manager_secret" "ci_spend" {
  for_each  = toset(["nori-ci-spend-token", "nori-ci-spend-management-key"])
  secret_id = each.value
  replication {
    auto {}
  }
  lifecycle { prevent_destroy = true }
}

resource "google_secret_manager_secret_iam_member" "ci_spend_management" {
  secret_id = google_secret_manager_secret.ci_spend["nori-ci-spend-management-key"].id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}

resource "google_secret_manager_secret_iam_member" "ci_spend_writer" {
  secret_id = google_secret_manager_secret.ci_spend["nori-ci-spend-token"].id
  role      = "roles/secretmanager.secretVersionAdder"
  member    = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}

resource "google_secret_manager_secret_iam_member" "ci_spend_runtime" {
  for_each = toset([
    "nori-sandbox-runtime@bot-111549.iam.gserviceaccount.com",
    "service-8273801370@gcp-sa-aiplatform-re.iam.gserviceaccount.com",
  ])
  secret_id = google_secret_manager_secret.ci_spend["nori-ci-spend-token"].id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${each.value}"
}

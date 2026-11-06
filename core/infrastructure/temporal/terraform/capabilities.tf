locals {
  capability_secrets = {
    BRAVE_SEARCH_API_KEY          = "nori-brave-search-api-key"
    GOOGLE_CALENDAR_CLIENT_ID     = "nori-google-calendar-client-id"
    GOOGLE_CALENDAR_CLIENT_SECRET = "nori-google-calendar-client-secret"
  }
}

# Terraform owns only these containers and references, never credential payloads.
# A release enables capabilities only once every container has a version.
resource "google_secret_manager_secret" "capability" {
  for_each  = local.production ? local.capability_secrets : {}
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

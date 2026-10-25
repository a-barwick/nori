# One native, paused-by-default shutdown-only job per retained pool. Jobs never
# start capacity. IAM denial precedes PATCH so WCI cannot race it back to one.
resource "google_service_account" "shutdown" {
  account_id   = "nori-temporal-shutdown"
  display_name = "Nori Temporal scheduled scale to zero"
}

resource "google_cloud_run_v2_worker_pool_iam_member" "shutdown" {
  for_each = google_cloud_run_v2_worker_pool.worker
  project  = local.project
  location = local.region
  name     = each.value.name
  role     = google_project_iam_custom_role.pool_scaler.name
  member   = "serviceAccount:${google_service_account.shutdown.email}"
}

resource "google_service_account_iam_member" "shutdown_runner" {
  service_account_id = "projects/${local.project}/serviceAccounts/${local.runner}"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.shutdown.email}"
}

resource "google_cloud_scheduler_job" "shutdown" {
  for_each         = google_cloud_run_v2_worker_pool.worker
  name             = "${each.value.name}-shutdown"
  region           = local.region
  description      = "Shutdown only; rearm for each explicit session and pause after verified scale-to-zero."
  schedule         = var.session == null ? "0 0 1 1 *" : formatdate("mm hh DD MM *", var.session.shutdown_at)
  time_zone        = "Etc/UTC"
  paused           = var.session == null
  attempt_deadline = "180s"
  http_target {
    http_method = "PATCH"
    uri         = "https://run.googleapis.com/v2/${each.value.id}?updateMask=scaling.manualInstanceCount"
    headers     = { "Content-Type" = "application/json" }
    body        = base64encode(jsonencode({ scaling = { manualInstanceCount = 0 } }))
    oauth_token {
      service_account_email = google_service_account.shutdown.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }
  retry_config {
    retry_count          = 5
    min_backoff_duration = "10s"
    max_backoff_duration = "60s"
    max_doublings        = 3
  }
  depends_on = [google_project_service.api, google_cloud_run_v2_worker_pool_iam_member.shutdown, google_service_account_iam_member.shutdown_runner]
}

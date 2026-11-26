# APIs Nori depends on. Removing one from this list leaves it enabled.
resource "google_project_service" "this" {
  for_each = toset([
    "artifactregistry.googleapis.com",
    "calendar-json.googleapis.com", # Google connection
    "cloudkms.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "compute.googleapis.com",
    "drive.googleapis.com", # Google connection
    "gmail.googleapis.com", # Google connection
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "iap.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
    "serviceusage.googleapis.com",
    "sqladmin.googleapis.com",
    "storage.googleapis.com",
    "sts.googleapis.com",
  ])

  service            = each.key
  disable_on_destroy = false
}

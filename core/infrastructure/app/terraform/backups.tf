resource "google_firestore_backup_schedule" "records" {
  count     = local.production ? 1 : 0
  project   = local.project
  database  = "nori-poc"
  retention = "604800s"
  daily_recurrence {}
}

resource "google_compute_resource_policy" "computer_data_backup" {
  count   = local.production ? 1 : 0
  project = local.project
  name    = "nori-computer-data-daily"
  region  = local.region
  snapshot_schedule_policy {
    schedule {
      daily_schedule {
        days_in_cycle = 1
        start_time    = "08:00"
      }
    }
    retention_policy {
      max_retention_days    = 7
      on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"
    }
    snapshot_properties {
      storage_locations = [local.region]
    }
  }
}

resource "google_compute_disk_resource_policy_attachment" "computer_data_backup" {
  count   = local.production ? 1 : 0
  project = local.project
  name    = google_compute_resource_policy.computer_data_backup[0].name
  disk    = data.google_compute_disk.computer.name
  zone    = data.google_compute_disk.computer.zone
}

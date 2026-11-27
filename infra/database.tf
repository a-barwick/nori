# Postgres for Core, Worker, migrations and the Home AI relay. Services connect
# through the Cloud SQL connector volume mounted at /cloudsql. The instance has
# no authorized networks, so nothing can connect to its public IP directly.
# Database users (nori, nori_home_ai_relay) and their passwords are managed in
# Postgres; their connection URLs live in the nori-postgres-url and
# nori-home-ai-db-url secrets.

resource "google_sql_database_instance" "nori_pg" {
  name                = "nori-pg"
  region              = local.region
  database_version    = "POSTGRES_17"
  deletion_protection = true

  settings {
    # Single-zone shared-core keeps the cost low. REGIONAL (high availability)
    # roughly doubles the instance price.
    tier              = "db-g1-small"
    edition           = "ENTERPRISE"
    availability_type = "ZONAL"
    disk_size         = 10
    disk_autoresize   = true

    enable_dataplex_integration = true

    # Blocks deleting the instance from the Console or gcloud, in addition to
    # deletion_protection above, which only stops Terraform.
    deletion_protection_enabled = true
    # Keeps the backups if the instance is deleted anyway.
    retain_backups_on_delete = true

    # Daily backup at 09:00 UTC (01:00 Pacific), seven kept, plus seven days of
    # transaction logs for point-in-time recovery.
    backup_configuration {
      enabled                        = true
      start_time                     = "09:00"
      point_in_time_recovery_enabled = true
      transaction_log_retention_days = 7

      backup_retention_settings {
        retained_backups = 7
        retention_unit   = "COUNT"
      }
    }

    # Google applies updates Sundays from 10:00 UTC (02:00 Pacific).
    maintenance_window {
      day          = 7
      hour         = 10
      update_track = "stable"
    }

    # Query Insights is free on Enterprise edition. Client addresses and
    # application tags stay off.
    insights_config {
      query_insights_enabled  = true
      record_client_address   = false
      record_application_tags = false
    }

    ip_configuration {
      ipv4_enabled = true
      ssl_mode     = "ALLOW_UNENCRYPTED_AND_ENCRYPTED"
    }

    location_preference {
      zone = "us-central1-c"
    }
  }
}

resource "google_sql_database" "nori" {
  name     = "nori"
  instance = google_sql_database_instance.nori_pg.name
}

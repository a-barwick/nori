# Postgres for Core, Worker, migrations and the Home AI relay. Services connect
# through the Cloud SQL connector volume mounted at /cloudsql.
# Database users (nori, nori_home_ai_relay) and their passwords are managed in
# Postgres; their connection URLs live in the nori-postgres-url and
# nori-home-ai-db-url secrets.

resource "google_sql_database_instance" "nori_pg" {
  name                = "nori-pg"
  region              = local.region
  database_version    = "POSTGRES_17"
  deletion_protection = true

  settings {
    tier              = "db-g1-small"
    edition           = "ENTERPRISE"
    availability_type = "ZONAL"
    disk_size         = 10
    disk_autoresize   = true

    enable_dataplex_integration = true

    backup_configuration {
      enabled    = false
      start_time = "22:00"
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

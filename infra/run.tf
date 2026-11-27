# Cloud Run services and jobs.
#
# Terraform owns each service's shape: identity, ingress, scaling, resources,
# probes, environment and secrets. scripts/release.sh owns what runs: it sets
# the image and the nori-commit and nori-prev labels on each new revision and
# moves traffic. Terraform ignores those fields so the two never fight.
#
# Changing a service's template here starts a new revision with the live image
# when CI applies it.

locals {
  sentry_core_dsn = "https://773c0d8018d7fac6907eab4b67f6800d@o4507087334670336.ingest.us.sentry.io/4512349076389888"
  sentry_web_dsn  = "https://45c82826f97d90746eee0c98a9a365db@o4507087334670336.ingest.us.sentry.io/4512349065838592"
  home_ai_release = "https://github.com/${local.github_repo}/releases/download/nori-v0.1.0"

  # Environment shared by Core, Worker and the migration job.
  runtime_env = {
    ARTIFACT_BUCKET              = google_storage_bucket.artifacts.name
    NORI_ALLOWED_EMAILS          = local.private.allowed_emails
    NORI_CLOUD                   = "gcp"
    NORI_CREDENTIAL_KMS_KEY      = local.kms_key
    NORI_CREDENTIAL_STORE        = "database"
    NORI_GOOGLE_OAUTH_CLIENT_IDS = "8273801370-im0dvqnf5v60e9djiba88uuvo5n52g9g.apps.googleusercontent.com"
    NORI_JEV_ENABLED             = "false"
    NORI_SERVICE_AUDIENCE        = "nori"
    NORI_STORE_BACKEND           = "postgres"
    SENTRY_DSN                   = local.sentry_core_dsn
    SENTRY_ENVIRONMENT           = "production"
  }
  runtime_secrets = {
    EXA_API_KEY            = "nori-exa-api-key:latest"
    JEV_API_KEY            = "nori-jev-api-key:latest"
    NORI_CREDENTIAL_KEY    = "nori-credential-key:latest"
    NORI_POSTGRES_URL      = "nori-postgres-url:latest"
    NORI_SERVICE_TOKEN_KEY = "nori-service-token-key:latest"
  }

  # Core and Worker also serve the Google connection and Home AI.
  feature_env = merge(
    {
      NORI_GOOGLE_WORKSPACE_CLIENT_ID    = "886773017404-30r5muoab6n8skn4sbkphpi5qkvbj4a4.apps.googleusercontent.com"
      NORI_GOOGLE_WORKSPACE_REDIRECT_URL = "${local.web_origin}/api/v1/connections/oauth/callback"
      NORI_HOME_AI_DOWNLOAD_LINUX_AMD64  = "${local.home_ai_release}/nori-connector-0.1.0-amd64.deb"
      NORI_HOME_AI_DOWNLOAD_LINUX_ARM64  = "${local.home_ai_release}/nori-connector-0.1.0-arm64.deb"
      NORI_HOME_AI_DOWNLOAD_MACOS        = "${local.home_ai_release}/Nori-0.1.0-arm64.dmg"
      NORI_HOME_AI_VERIFY_URL            = "${local.web_origin}/connector/activate"
      NORI_HOME_AI_WORK_ENABLED          = "true"
    },
    # An empty tester list offers Google to every user.
    local.private.google_workspace_test_users == "" ? {} : {
      NORI_GOOGLE_WORKSPACE_TEST_USERS = local.private.google_workspace_test_users
    },
  )
  feature_secrets = {
    NORI_GOOGLE_WORKSPACE_CLIENT_SECRET = "nori-google-workspace-beta-client-secret:1"
    NORI_HOME_AI_KEY                    = "nori-home-ai-key:1"
  }

  # Each map is NAME => { value } or NAME => { secret, version }, sorted by name.
  env = {
    core = merge(
      { for k, v in merge(local.runtime_env, local.feature_env, {
        NORI_HOME_AI_MAX_CONNS  = "4"
        NORI_HOME_AI_RELAY_HOST = "relay.noribot.chat"
        NORI_POSTGRES_MAX_CONNS = "8"
        NORI_ROLE               = "all"
      }) : k => { value = v, secret = null } },
      { for k, v in merge(local.runtime_secrets, local.feature_secrets) : k => { value = null, secret = v } },
    )
    migrate = merge(
      { for k, v in merge(local.runtime_env, { NORI_ROLE = "all", NORI_POSTGRES_MAX_CONNS = "4" }) : k => { value = v, secret = null } },
      { for k, v in local.runtime_secrets : k => { value = null, secret = v } },
    )
    home_ai_migrate = merge(
      { for k, v in { ARTIFACT_BUCKET = google_storage_bucket.artifacts.name, NORI_CLOUD = "gcp", NORI_POSTGRES_MAX_CONNS = "2", NORI_STORE_BACKEND = "postgres" } : k => { value = v, secret = null } },
      { NORI_POSTGRES_URL = { value = null, secret = "nori-postgres-url:latest" } },
    )
    web = merge(
      { for k, v in {
        IAP_AUDIENCE          = "/projects/${local.project_number}/locations/${local.region}/services/nori-web"
        NORI_AUTH_PROVIDER    = "iap"
        NORI_CLOUD            = "gcp"
        NORI_RUNTIME_URL      = local.core_url
        NORI_SERVICE_AUDIENCE = "nori"
        SENTRY_ENVIRONMENT    = "production"
        SENTRY_WEB_DSN        = local.sentry_web_dsn
      } : k => { value = v, secret = null } },
      { NORI_SERVICE_TOKEN_KEY = { value = null, secret = "nori-service-token-key:latest" } },
    )
    web_mac = merge(
      { for k, v in {
        NORI_AUTH_PROVIDER            = "oidc"
        NORI_CLOUD                    = "gcp"
        NORI_OIDC_AUDIENCE            = "8273801370-mo25ms2619hr4mjt4jf25i6f6r7os6ld.apps.googleusercontent.com"
        NORI_OIDC_IAP_POLICY_RESOURCE = "projects/${local.project_number}/iap_web/cloud_run-${local.region}/services/nori-web"
        NORI_OIDC_ISSUER              = "https://accounts.google.com"
        NORI_RUNTIME_URL              = local.core_url
        NORI_SERVICE_AUDIENCE         = "nori"
      } : k => { value = v, secret = null } },
      {
        NORI_OIDC_IAP_ALLOWED_IDENTITIES = { value = null, secret = "nori-mac-google-identities:1" }
        NORI_SERVICE_TOKEN_KEY           = { value = null, secret = "nori-service-token-key:latest" }
      },
    )
    relay = merge(
      { for k, v in { NORI_HOME_AI_MAX_CONNS = "4", NORI_HOME_AI_VERIFY_URL = "${local.web_origin}/connector/activate" } : k => { value = v, secret = null } },
      {
        NORI_HOME_AI_KEY          = { value = null, secret = "nori-home-ai-key:1" }
        NORI_HOME_AI_POSTGRES_URL = { value = null, secret = "nori-home-ai-db-url:1" }
      },
    )
  }
}

# --- Core API ------------------------------------------------------------------

resource "google_cloud_run_v2_service" "nori_core" {
  name     = "nori-core"
  location = local.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  scaling {
    max_instance_count = 3
  }

  template {
    service_account                  = google_service_account.nori_runtime.email
    max_instance_request_concurrency = 80
    timeout                          = "3600s"

    scaling {
      min_instance_count = 1
      max_instance_count = 2
    }

    containers {
      image = "${local.registry}/nori-core:bootstrap"

      dynamic "env" {
        for_each = local.env.core
        content {
          name  = env.key
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.secret == null ? [] : [split(":", env.value.secret)]
            content {
              secret_key_ref {
                secret  = value_source.value[0]
                version = value_source.value[1]
              }
            }
          }
        }
      }

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        cpu_idle          = false
        startup_cpu_boost = true
        limits = {
          cpu    = "1"
          memory = "1Gi"
        }
      }

      startup_probe {
        failure_threshold = 40
        period_seconds    = 3
        timeout_seconds   = 3
        http_get {
          path = "/readyz"
          port = 8080
        }
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [local.cloud_sql]
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, traffic, template[0].labels, template[0].containers[0].image]
  }
}

# The API checks HS256 service tokens itself; Cloud Run lets anyone reach it.
resource "google_cloud_run_v2_service_iam_member" "core_public" {
  name     = google_cloud_run_v2_service.nori_core.name
  location = local.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# --- Migration jobs ------------------------------------------------------------

# Release runs this with each Core image before staging Core.
resource "google_cloud_run_v2_job" "nori_migrate" {
  name     = "nori-migrate"
  location = local.region

  template {
    task_count = 1

    template {
      service_account       = google_service_account.nori_runtime.email
      execution_environment = "EXECUTION_ENVIRONMENT_GEN2"
      max_retries           = 1
      timeout               = "300s"

      containers {
        image = "${local.registry}/nori-core:bootstrap"
        args  = ["--migrate-only"]

        dynamic "env" {
          for_each = local.env.migrate
          content {
            name  = env.key
            value = env.value.value
            dynamic "value_source" {
              for_each = env.value.secret == null ? [] : [split(":", env.value.secret)]
              content {
                secret_key_ref {
                  secret  = value_source.value[0]
                  version = value_source.value[1]
                }
              }
            }
          }
        }

        resources {
          limits = {
            cpu    = "1"
            memory = "1Gi"
          }
        }

        volume_mounts {
          name       = "cloudsql"
          mount_path = "/cloudsql"
        }
      }

      volumes {
        name = "cloudsql"
        cloud_sql_instance {
          instances = [local.cloud_sql]
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, template[0].template[0].containers[0].image]
  }
}

# Applies Core migrations for the Home AI relay rollout (core/infrastructure/home-ai.md).
resource "google_cloud_run_v2_job" "nori_home_ai_migrate" {
  name     = "nori-home-ai-migrate"
  location = local.region

  template {
    task_count = 1

    template {
      service_account       = google_service_account.nori_runtime.email
      execution_environment = "EXECUTION_ENVIRONMENT_GEN2"
      max_retries           = 0
      timeout               = "300s"

      containers {
        image = "${local.registry}/nori-core:bootstrap"
        args  = ["--migrate-only"]

        dynamic "env" {
          for_each = local.env.home_ai_migrate
          content {
            name  = env.key
            value = env.value.value
            dynamic "value_source" {
              for_each = env.value.secret == null ? [] : [split(":", env.value.secret)]
              content {
                secret_key_ref {
                  secret  = value_source.value[0]
                  version = value_source.value[1]
                }
              }
            }
          }
        }

        resources {
          limits = {
            cpu    = "1000m"
            memory = "512Mi"
          }
        }

        volume_mounts {
          name       = "cloudsql"
          mount_path = "/cloudsql"
        }
      }

      volumes {
        name = "cloudsql"
        cloud_sql_instance {
          instances = [local.cloud_sql]
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, template[0].template[0].containers[0].image]
  }
}

# --- Web -----------------------------------------------------------------------

# Browser Web behind IAP at app.noribot.chat.
resource "google_cloud_run_v2_service" "nori_web" {
  name        = "nori-web"
  location    = local.region
  ingress     = "INGRESS_TRAFFIC_ALL"
  iap_enabled = true

  scaling {
    max_instance_count = 1
  }

  template {
    service_account                  = google_service_account.nori_web.email
    max_instance_request_concurrency = 10
    timeout                          = "3600s"

    scaling {
      max_instance_count = 2
    }

    containers {
      image = "${local.registry}/nori-web:bootstrap"

      dynamic "env" {
        for_each = local.env.web
        content {
          name  = env.key
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.secret == null ? [] : [split(":", env.value.secret)]
            content {
              secret_key_ref {
                secret  = value_source.value[0]
                version = value_source.value[1]
              }
            }
          }
        }
      }

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        cpu_idle          = true
        startup_cpu_boost = false
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
      }

      startup_probe {
        failure_threshold = 40
        period_seconds    = 3
        timeout_seconds   = 3
        http_get {
          path = "/api/health"
          port = 8080
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, traffic, template[0].labels, template[0].containers[0].image]
  }
}

resource "google_cloud_run_v2_service_iam_member" "web_iap" {
  name     = google_cloud_run_v2_service.nori_web.name
  location = local.region
  role     = "roles/run.invoker"
  member   = "serviceAccount:service-${local.project_number}@gcp-sa-iap.iam.gserviceaccount.com"
}

# Mac Web reads the IAP access policy to decide who may sign in. Grants to
# people (roles/iap.httpsResourceAccessor) are user access, managed with
# `gcloud iap web add-iam-policy-binding` rather than here.
resource "google_iap_web_cloud_run_service_iam_member" "web_mac_policy_reader" {
  location               = local.region
  cloud_run_service_name = google_cloud_run_v2_service.nori_web.name
  role                   = google_project_iam_custom_role.mac_iap_policy_reader.id
  member                 = google_service_account.nori_web_mac.member
}

# Web for Nori for Mac. It verifies Google ID tokens itself, so Cloud Run skips
# its invoker check.
resource "google_cloud_run_v2_service" "nori_web_mac" {
  name                 = "nori-web-mac"
  location             = local.region
  ingress              = "INGRESS_TRAFFIC_ALL"
  invoker_iam_disabled = true

  scaling {
    max_instance_count = 20
  }

  template {
    service_account                  = google_service_account.nori_web_mac.email
    max_instance_request_concurrency = 80
    timeout                          = "300s"

    scaling {
      max_instance_count = 2
    }

    containers {
      image = "${local.registry}/nori-web:bootstrap"

      dynamic "env" {
        for_each = local.env.web_mac
        content {
          name  = env.key
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.secret == null ? [] : [split(":", env.value.secret)]
            content {
              secret_key_ref {
                secret  = value_source.value[0]
                version = value_source.value[1]
              }
            }
          }
        }
      }

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        cpu_idle          = true
        startup_cpu_boost = true
        limits = {
          cpu    = "1000m"
          memory = "512Mi"
        }
      }

      startup_probe {
        failure_threshold = 40
        period_seconds    = 3
        timeout_seconds   = 3
        http_get {
          path = "/api/health"
          port = 8080
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, traffic, template[0].labels, template[0].containers[0].image]
  }
}

# --- Home AI relay -------------------------------------------------------------

# Reachable only through the load balancer in home_ai_edge.tf. Its image is
# deployed by scripts/deploy-home-ai-relay.sh.
resource "google_cloud_run_v2_service" "nori_connector_relay" {
  name                 = "nori-connector-relay"
  location             = local.region
  ingress              = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
  default_uri_disabled = true
  invoker_iam_disabled = true

  scaling {
    max_instance_count = 1
  }

  template {
    service_account                  = google_service_account.nori_home_ai_relay.email
    max_instance_request_concurrency = 40
    timeout                          = "3600s"

    scaling {
      max_instance_count = 1
    }

    containers {
      image = "${local.registry}/nori-connector-relay:bootstrap"

      dynamic "env" {
        for_each = local.env.relay
        content {
          name  = env.key
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.secret == null ? [] : [split(":", env.value.secret)]
            content {
              secret_key_ref {
                secret  = value_source.value[0]
                version = value_source.value[1]
              }
            }
          }
        }
      }

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        cpu_idle          = true
        startup_cpu_boost = true
        limits = {
          cpu    = "1000m"
          memory = "512Mi"
        }
      }

      startup_probe {
        failure_threshold = 40
        period_seconds    = 3
        timeout_seconds   = 3
        http_get {
          path = "/readyz"
          port = 8080
        }
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [local.cloud_sql]
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, traffic, template[0].labels, template[0].containers[0].image]
  }
}

# --- Public site ---------------------------------------------------------------

# Public site at www.noribot.chat, built from site/. Releases ship its image.
resource "google_cloud_run_v2_service" "nori_site" {
  name     = "nori-site"
  location = local.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  scaling {
    max_instance_count = 20
  }

  template {
    service_account                  = google_service_account.nori_site.email
    max_instance_request_concurrency = 80
    timeout                          = "300s"

    scaling {
      max_instance_count = 10
    }

    containers {
      image = "${local.registry}/nori-site:bootstrap"

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        cpu_idle          = true
        startup_cpu_boost = true
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }
      }

      startup_probe {
        failure_threshold = 40
        period_seconds    = 3
        timeout_seconds   = 3
        http_get {
          path = "/healthz"
          port = 8080
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version, traffic, template[0].labels, template[0].containers[0].image]
  }
}

resource "google_cloud_run_v2_service_iam_member" "site_public" {
  name     = google_cloud_run_v2_service.nori_site.name
  location = local.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# --- Domains -------------------------------------------------------------------
# DNS for noribot.chat is hosted at Porkbun: the apex, app and www point at
# ghs.googlehosted.com, and relay points at the Home AI edge address.

# The bare domain serves the public site, whose nginx sends site pages to www and
# every other path to app.noribot.chat with a 308.
resource "google_cloud_run_domain_mapping" "noribot_chat" {
  name     = "noribot.chat"
  location = local.region

  metadata {
    namespace = local.project
  }

  spec {
    route_name = google_cloud_run_v2_service.nori_site.name
  }

  # The API omits certificate_mode until the certificate is issued.
  lifecycle {
    ignore_changes = [spec[0].certificate_mode]
  }
}

resource "google_cloud_run_domain_mapping" "app_noribot_chat" {
  name     = "app.noribot.chat"
  location = local.region

  metadata {
    namespace = local.project
  }

  spec {
    route_name = google_cloud_run_v2_service.nori_web.name
  }

  # The API omits certificate_mode until the certificate is issued.
  lifecycle {
    ignore_changes = [spec[0].certificate_mode]
  }
}

resource "google_cloud_run_domain_mapping" "www_noribot_chat" {
  name     = "www.noribot.chat"
  location = local.region

  metadata {
    namespace = local.project
  }

  spec {
    route_name = google_cloud_run_v2_service.nori_site.name
  }

  # The API omits certificate_mode until the certificate is issued.
  lifecycle {
    ignore_changes = [spec[0].certificate_mode]
  }
}

resource "google_cloud_run_domain_mapping" "relay_noribot_chat" {
  name     = "relay.noribot.chat"
  location = local.region

  metadata {
    namespace = local.project
  }

  spec {
    route_name = google_cloud_run_v2_service.nori_core.name
  }

  # The API omits certificate_mode until the certificate is issued.
  lifecycle {
    ignore_changes = [spec[0].certificate_mode]
  }
}

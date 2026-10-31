locals {
  project                 = "bot-111549"
  region                  = "us-west1"
  production              = var.environment == "production"
  runner                  = "${local.production ? "nori-poc" : "nori-sandbox"}-runtime@${local.project}.iam.gserviceaccount.com"
  deployment              = local.production ? "nori-task" : "nori-sandbox-task"
  task_queue              = local.production ? "nori-tasks" : "nori-sandbox-tasks"
  conversation_name       = local.production ? "nori-conversation" : "nori-sandbox-conversation"
  temporal_api_key_secret = "nori-temporal-api-key"
  # Copied from this Temporal account's Access template, not generic Temporal identities.
  temporal_impersonators = toset([
    "serverless-qtjow@prod-vs4padk9dkczcey6d6st9c58e.iam.gserviceaccount.com",
    "serverless-qtjow@prod-sbe56h6wr4sykaw47a6yg4h66.iam.gserviceaccount.com",
    "serverless-qtjow@prod-97gr6tp6zkv2t4epubffpz7kt.iam.gserviceaccount.com",
    "serverless-qtjow@prod-c4cqaqgbfzy23vpzkbxzrx378.iam.gserviceaccount.com",
    "serverless-qtjow@prod-79z2c6d76au6g5fcq768buqf9.iam.gserviceaccount.com",
  ])
  worker_env = {
    NORI_PROJECT_ID       = local.project
    FIRESTORE_DATABASE_ID = local.production ? "nori-poc" : "nori-sandbox"
    ARTIFACT_BUCKET       = local.production ? "bot-111549-nori-poc" : "bot-111549-nori-sandbox"
    # The worker uses the runtime identity, whose queue enqueue and wake-caller
    # actAs grants are owned by the POC Terraform state (not duplicated here).
    NORI_WAKE_QUEUE           = "projects/${local.project}/locations/us-central1/queues/${local.conversation_name}"
    NORI_WAKE_SERVICE_ACCOUNT = "${local.conversation_name}-wake@${local.project}.iam.gserviceaccount.com"
    NORI_RUNTIME_URL          = var.runtime_invoke_root
    COMPUTER_ZONE             = "us-central1-a"
    COMPUTER_INSTANCE         = local.production ? "nori-computer" : "nori-sandbox-computer"
    MODEL_LOCATION            = "global"
    MODEL_NAME                = "gemini-3.8-flash"
    MODEL_INPUT_USD_PER_MTOK  = "0.75"
    MODEL_OUTPUT_USD_PER_MTOK = "3.75"
    TEMPORAL_ADDRESS          = "quickstart-nori.qtjow.tmprl.cloud:7233"
    TEMPORAL_NAMESPACE        = "quickstart-nori.qtjow"
    TEMPORAL_TASK_QUEUE       = local.task_queue
    TEMPORAL_DEPLOYMENT_NAME  = local.deployment
    TEMPORAL_CLOUD_RUN_ID     = "true"
  }
}

resource "google_project_service" "api" {
  for_each           = local.production ? toset(["secretmanager.googleapis.com"]) : toset([])
  project            = local.project
  service            = each.value
  disable_on_destroy = false
}

# Only the empty secret container is Terraform-managed. Upload a version through
# native Secret Manager API from the root .env without logging the payload.
resource "google_secret_manager_secret" "temporal_api_key" {
  count     = local.production ? 1 : 0
  secret_id = local.temporal_api_key_secret
  replication {
    auto {}
  }
  lifecycle {
    prevent_destroy = true
  }
  depends_on = [google_project_service.api]
}

resource "google_secret_manager_secret_iam_member" "runner_key" {
  secret_id  = "projects/${local.project}/secrets/${local.temporal_api_key_secret}"
  role       = "roles/secretmanager.secretAccessor"
  member     = "serviceAccount:${local.runner}"
  depends_on = [google_secret_manager_secret.temporal_api_key]
}

resource "google_cloud_run_v2_worker_pool" "worker" {
  for_each            = var.worker_images
  name                = "${local.deployment}-${each.key}"
  location            = local.region
  deletion_protection = true
  # MANUAL mode uses manual_instance_count. Temporal version configuration
  # supplies WCI max=1; native min/max do not constrain the manual count.
  scaling {
    scaling_mode          = "MANUAL"
    manual_instance_count = 0
  }
  template {
    service_account = local.runner
    containers {
      image = each.value
      resources {
        limits = { cpu = "1", memory = "512Mi" }
      }
      dynamic "env" {
        for_each = merge(local.worker_env, { TEMPORAL_BUILD_ID = each.key })
        content {
          name  = env.key
          value = env.value
        }
      }
      env {
        name = "TEMPORAL_API_KEY"
        value_source {
          secret_key_ref {
            secret  = local.temporal_api_key_secret
            version = "latest"
          }
        }
      }
    }
  }
  lifecycle {
    prevent_destroy = true
    # WCI and the operator pause command own count after creation. Terraform never
    # restarts paid capacity just because it refreshes an active pool.
    ignore_changes = [scaling[0].manual_instance_count]
  }
  depends_on = [google_secret_manager_secret_iam_member.runner_key]
}

resource "google_service_account" "invoker" {
  account_id   = local.production ? "nori-temporal-invoker" : "nori-sandbox-temporal-invoker"
  display_name = "Temporal WCI scaler (not worker runtime)"
}

resource "google_project_iam_custom_role" "pool_scaler" {
  role_id     = local.production ? "noriTemporalPoolScaler" : "noriSandboxTemporalPoolScaler"
  title       = "Nori Temporal pool read and scale"
  permissions = ["run.workerpools.get", "run.workerpools.update"]
}

# Stable resource-scoped IAM. Removing a version's native compute configuration
# pauses scaling without changing identities or deleting pinned workflows.
resource "google_cloud_run_v2_worker_pool_iam_member" "wci" {
  for_each = { for build, pool in google_cloud_run_v2_worker_pool.worker : build => pool if contains(var.scalable_build_ids, build) }
  project  = local.project
  location = local.region
  name     = each.value.name
  role     = google_project_iam_custom_role.pool_scaler.name
  member   = "serviceAccount:${google_service_account.invoker.email}"
}

resource "google_service_account_iam_member" "wci_runner" {
  service_account_id = "projects/${local.project}/serviceAccounts/${local.runner}"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.invoker.email}"
}

resource "google_service_account_iam_member" "temporal_impersonation" {
  for_each           = local.temporal_impersonators
  service_account_id = google_service_account.invoker.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${each.value}"
}

moved {
  from = google_secret_manager_secret.temporal_api_key
  to   = google_secret_manager_secret.temporal_api_key[0]
}

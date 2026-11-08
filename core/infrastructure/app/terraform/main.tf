locals {

  # Shared with ops.py so native releases apply the same capacity settings.
  runtime_settings = jsondecode(file("${path.module}/../runtime-settings.json"))

  production        = var.environment == "production"
  prefix            = local.production ? "nori-poc" : "nori-sandbox"
  web_name          = local.production ? "nori-web" : "nori-sandbox-web"
  computer_name     = local.production ? "nori-computer" : "nori-sandbox-computer"
  database          = coalesce(var.firestore_database, local.production ? "nori-poc" : "nori-sandbox")
  conversation_name = local.production ? "nori-conversation" : "nori-sandbox-conversation"
  conversation_secrets = {
    OPENAI_API_KEY     = local.production ? "nori-openai-api-key" : "nori-sandbox-openai-api-key"
    OPENROUTER_API_KEY = local.production ? "nori-openrouter-api-key" : "nori-sandbox-openrouter-api-key"
    JEV_API_KEY        = "nori-jev-api-key"
  }
  task_queue          = local.production ? "nori-tasks" : "nori-sandbox-tasks"
  temporal_deployment = local.production ? "nori-task" : "nori-sandbox-task"
  project             = "bot-111549"
  region              = "us-central1"
  zone                = "us-central1-a"
  number              = "8273801370"
  apis                = toset(["run.googleapis.com", "aiplatform.googleapis.com", "cloudtasks.googleapis.com", "firestore.googleapis.com", "storage.googleapis.com", "artifactregistry.googleapis.com", "compute.googleapis.com", "iap.googleapis.com", "iam.googleapis.com", "iamcredentials.googleapis.com", "policytroubleshooter.googleapis.com"])
  runtime_name        = "projects/${local.number}/locations/${local.region}/reasoningEngines/${basename(google_vertex_ai_reasoning_engine.runtime.name)}"
  runtime_url         = "https://${local.region}-aiplatform.googleapis.com/reasoningEngines/v1/${local.runtime_name}/api"

}

variable "environment" {
  type    = string
  default = "production"
  validation {
    condition     = contains(["production", "sandbox"], var.environment)
    error_message = "Environment must be production or sandbox."
  }
}

variable "sandbox_computer_disk_id" {
  type        = string
  default     = null
  description = "Numeric identity output by the separately applied sandbox foundation; required for sandbox, never inferred from a disk name."
  validation {
    condition     = var.environment == "production" ? var.sandbox_computer_disk_id == null : can(regex("^[0-9]+$", var.sandbox_computer_disk_id))
    error_message = "Sandbox requires the foundation retained_disk_numeric_id output; production must omit this input."
  }
}

variable "web_image" {
  type = string
}

variable "runtime_image" {
  type = string
}

# Existing runtime_url output; supplied explicitly because the runtime cannot
# refer to its own generated resource name in its deployment environment.
variable "runtime_invoke_root" {
  type        = string
  default     = ""
  description = "Native Agent Runtime invoke root ending in /api; required before deploying the durable conversation image."
}

variable "worker_services" {
  description = "Active environment build/URL map from the reviewed Temporal production_worker_services output. Retain old pinned routes; exclude isolated candidate builds."
  type        = map(string)
  default     = {}
  validation {
    condition = alltrue([for build, url in var.worker_services :
      can(regex("^[a-z0-9][a-z0-9-]{0,39}$", build)) && url == "https://${var.environment == "production" ? "nori-task" : "nori-sb"}-${build}-8273801370.us-west1.run.app"
    ])
    error_message = "Use the complete reviewed request-worker build/URL map for the selected environment."
  }
}

variable "worker_wake_queue" {
  description = "Worker wake queue output from the reviewed Temporal infrastructure for this environment."
  type        = string
  default     = ""
  validation {
    condition = (length(var.worker_services) == 0 && var.worker_wake_queue == "") || (
      length(var.worker_services) > 0 && var.worker_wake_queue == "projects/bot-111549/locations/us-west1/queues/${var.environment == "production" ? "nori-worker" : "nori-sandbox-worker"}"
    )
    error_message = "Configure the worker route map and its environment-specific wake queue together."
  }
}

variable "outer_model" {
  type    = string
  default = "gpt-6.1-sol"
}

variable "computer_release_sha256" {

  type = string
  validation {

    condition     = can(regex("^[a-f0-9]{64}$", var.computer_release_sha256))
    error_message = "Use the sha256 of the statically built Linux amd64 computer-agent."

  }

}

variable "model" {
  type    = string
  default = "openai/gpt-6.1-terra"
}

variable "firestore_database" {
  type        = string
  default     = null
  description = "Defaults to nori-poc in production and nori-sandbox in sandbox."
  validation {
    condition     = var.firestore_database == null ? true : var.firestore_database == (var.environment == "production" ? "nori-poc" : "nori-sandbox")
    error_message = "Database must match the selected environment."
  }
}

resource "google_project_service" "api" {

  for_each           = local.production ? local.apis : toset([])
  project            = local.project
  service            = each.value
  disable_on_destroy = false

}

data "google_compute_network" "computer" {
  name = local.computer_name
}

data "google_compute_subnetwork" "computer" {
  name   = local.computer_name
  region = local.region
}

data "google_compute_disk" "computer" {
  name = local.production ? "bot-computer-data" : "nori-sandbox-computer-data"
  zone = local.zone
}

resource "google_artifact_registry_repository" "images" {
  count = local.production ? 1 : 0

  location      = local.region
  repository_id = "nori-poc"
  format        = "DOCKER"
  depends_on    = [google_project_service.api]

}

resource "google_storage_bucket" "artifacts" {

  name                        = "bot-111549-${local.prefix}"
  location                    = "US-CENTRAL1"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false
  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.api]

}

resource "google_firestore_database" "state" {

  name                    = local.database
  location_id             = local.region
  type                    = "FIRESTORE_NATIVE"
  delete_protection_state = "DELETE_PROTECTION_ENABLED"
  deletion_policy         = "ABANDON"
  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.api]

}

resource "google_service_account" "web" {
  account_id   = "${local.prefix}-web"
  display_name = "Nori web"
}

resource "google_service_account" "runtime" {
  account_id   = "${local.prefix}-runtime"
  display_name = "Nori agent runtime"
}

resource "google_service_account" "conversation_wake" {
  account_id   = "${local.conversation_name}-wake"
  display_name = "Nori conversation queue caller"
}

# Secret Manager is already enabled and owned by the Temporal state. Terraform
# owns only these retained containers and references, never credential payloads.
resource "google_secret_manager_secret" "conversation" {
  for_each  = local.production ? local.conversation_secrets : {}
  secret_id = each.value
  replication {
    auto {}
  }
  lifecycle {
    prevent_destroy = true
  }
}

resource "google_secret_manager_secret_iam_member" "conversation_runtime_secrets" {
  for_each   = local.conversation_secrets
  secret_id  = each.value
  role       = "roles/secretmanager.secretAccessor"
  member     = "serviceAccount:${google_service_account.runtime.email}"
  depends_on = [google_secret_manager_secret.conversation]
}

# Agent Platform resolves secret environment variables during deployment; the
# custom runtime identity also needs access when the container is started.
resource "google_secret_manager_secret_iam_member" "conversation_deploy_secrets" {
  for_each  = google_secret_manager_secret.conversation
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:service-${local.number}@gcp-sa-aiplatform.iam.gserviceaccount.com"
}

resource "google_project_iam_member" "cloudtasks_service_agent" {
  count      = local.production ? 1 : 0
  project    = local.project
  role       = "roles/cloudtasks.serviceAgent"
  member     = "serviceAccount:service-${local.number}@gcp-sa-cloudtasks.iam.gserviceaccount.com"
  depends_on = [google_project_service.api]
}

resource "google_cloud_tasks_queue" "conversation" {
  name     = local.conversation_name
  location = local.region
  rate_limits {
    max_concurrent_dispatches = 1
    max_dispatches_per_second = 1
  }
  retry_config {
    max_attempts       = -1
    max_retry_duration = "0s"
    min_backoff        = "1s"
    max_backoff        = "60s"
    max_doublings      = 6
  }
  depends_on = [google_project_service.api]
}

resource "google_cloud_tasks_queue_iam_member" "runtime_enqueue" {
  project  = local.project
  location = local.region
  name     = google_cloud_tasks_queue.conversation.name
  role     = "roles/cloudtasks.enqueuer"
  member   = "serviceAccount:${google_service_account.runtime.email}"
}

resource "google_service_account_iam_member" "runtime_wake_caller" {
  service_account_id = google_service_account.conversation_wake.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.runtime.email}"
}

# Agent Runtime does not expose resource.name to inherited IAM conditions.
# Grant this one-permission role directly on the retained engine instead.
resource "google_vertex_ai_reasoning_engine_iam_member" "wake_runtime_query" {
  project          = local.project
  region           = local.region
  reasoning_engine = google_vertex_ai_reasoning_engine.runtime.name
  role             = google_project_iam_custom_role.query.name
  member           = "serviceAccount:${google_service_account.conversation_wake.email}"
}

resource "google_service_account" "computer" {
  account_id   = "${local.prefix}-computer"
  display_name = "Nori retained computer"
}

resource "google_project_iam_custom_role" "query" {

  role_id     = local.production ? "noriPocRuntimeQuery" : "noriSandboxRuntimeQuery"
  title       = "Nori runtime invocation"
  permissions = ["aiplatform.reasoningEngines.query"]

}

resource "google_project_iam_custom_role" "computer_start" {

  role_id     = local.production ? "noriPocComputerStart" : "noriSandboxComputerStart"
  title       = "Nori retained computer start"
  permissions = ["compute.instances.get", "compute.instances.start"]

}

resource "google_project_iam_member" "computer_start" {

  project = local.project
  role    = google_project_iam_custom_role.computer_start.name
  member  = "serviceAccount:${google_service_account.runtime.email}"
  condition {

    title      = "nori-computer-only"
    expression = "resource.name == 'projects/${local.project}/zones/${local.zone}/instances/${local.computer_name}'"

  }

}

resource "google_project_iam_member" "runtime_firestore" {

  project = local.project
  role    = "roles/datastore.user"
  member  = "serviceAccount:${google_service_account.runtime.email}"
  condition {

    title      = "nori-state-only"
    expression = "resource.name == 'projects/${local.project}/databases/${local.database}' || resource.name.startsWith('projects/${local.project}/databases/${local.database}/') || resource.name == 'projects/${local.number}/databases/${local.database}' || resource.name.startsWith('projects/${local.number}/databases/${local.database}/')"

  }

}

resource "google_project_iam_custom_role" "model" {
  role_id     = local.production ? "noriPocModelPredict" : "noriSandboxModelPredict"
  title       = "Nori runtime API calls"
  permissions = ["aiplatform.endpoints.predict", "serviceusage.services.use"]
}
resource "google_project_iam_member" "runtime_model" {

  project = local.project
  role    = google_project_iam_custom_role.model.name
  member  = "serviceAccount:${google_service_account.runtime.email}"

}

resource "google_storage_bucket_iam_member" "runtime_objects" {

  bucket = google_storage_bucket.artifacts.name
  role   = "roles/storage.objectUser"
  member = "serviceAccount:${google_service_account.runtime.email}"
  condition {

    title      = "runtime-artifacts-only"
    expression = "resource.name.startsWith('projects/_/buckets/${google_storage_bucket.artifacts.name}/objects/artifacts/')"

  }

}

resource "google_storage_bucket_iam_member" "computer_release" {

  bucket = google_storage_bucket.artifacts.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.computer.email}"
  condition {

    title      = "computer-binaries-only"
    expression = "resource.name.startsWith('projects/_/buckets/${google_storage_bucket.artifacts.name}/objects/releases/computer-agent-')"

  }

}

resource "google_compute_firewall" "computer_rpc" {

  name                    = "${local.prefix}-computer-iap-rpc"
  network                 = data.google_compute_network.computer.id
  source_ranges           = ["35.235.240.0/20"]
  target_service_accounts = [google_service_account.computer.email]
  allow {
    protocol = "tcp"
    ports    = ["8090"]
  }

}

resource "google_compute_instance" "computer" {

  name                      = local.computer_name
  tags                      = local.production ? [] : [local.computer_name]
  zone                      = local.zone
  machine_type              = "n2-standard-2"
  min_cpu_platform          = "Intel Cascade Lake"
  desired_status            = "TERMINATED"
  allow_stopping_for_update = false
  deletion_protection       = true
  advanced_machine_features {
    enable_nested_virtualization = true
  }

  boot_disk {

    auto_delete = true
    initialize_params {
      image = "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64"
      type  = "pd-standard"
      size  = 20
    }

  }

  attached_disk {
    source      = data.google_compute_disk.computer.id
    device_name = "bot-guest-data"
    mode        = "READ_WRITE"
  }

  network_interface {
    subnetwork = data.google_compute_subnetwork.computer.id
    access_config {

    }

  }

  service_account {
    email  = google_service_account.computer.email
    scopes = ["https://www.googleapis.com/auth/devstorage.read_only"]
  }

  scheduling {

    automatic_restart   = false
    on_host_maintenance = "TERMINATE"
    provisioning_model  = "STANDARD"

  }

  metadata = {

    block-project-ssh-keys = "true"
    enable-oslogin         = "true"
    serial-port-enable     = "false"
    startup-script         = file("${path.module}/../computer-startup.sh")
    shutdown-script        = "#!/bin/bash\nset -e\n[ ! -x /usr/local/sbin/nori-vm ] || /usr/local/sbin/nori-vm stop\n"
    # Keep the deployed attachment's device name; host paths/services use Nori.
    nori-vm-control = replace(file("${path.module}/../../microvm/vm-control.sh"), "/dev/disk/by-id/google-nori-guest-data", "/dev/disk/by-id/google-bot-guest-data")
    computer-paused = "true"
    computer-telemetry = jsonencode({
      SENTRY_DSN                = var.observability.core_dsn
      SENTRY_ENVIRONMENT        = var.environment
      SENTRY_RELEASE            = var.computer_release_sha256
      SENTRY_TRACES_SAMPLE_RATE = tostring(var.observability.trace_sample_rate)
    })
    computer-release-object = "${google_storage_bucket.artifacts.name}/releases/computer-agent-${var.computer_release_sha256}"
    computer-release-sha256 = var.computer_release_sha256

  }

  lifecycle {

    prevent_destroy = true
    # Google 8.8.1 replaces the instance when termination_time is removed.
    # The native clear-deadline migration owns these two legacy fields instead;
    # ops.py refuses unpause while any termination schedule remains.
    ignore_changes = [desired_status, scheduling[0].termination_time, scheduling[0].instance_termination_action, metadata["computer-paused"], metadata["computer-release-object"], metadata["computer-release-sha256"]]
    precondition {

      condition     = data.google_compute_disk.computer.disk_id == (local.production ? "3757334953750232874" : var.sandbox_computer_disk_id) && data.google_compute_disk.computer.size == 20
      error_message = "Refusing to attach a replaced or resized retained guest disk."

    }

  }

  depends_on = [google_project_service.api]

}

resource "google_iap_tunnel_instance_iam_member" "runtime_computer" {

  project  = local.project
  zone     = local.zone
  instance = google_compute_instance.computer.name
  role     = "roles/iap.tunnelResourceAccessor"
  member   = "serviceAccount:${google_service_account.runtime.email}"
  condition {
    title      = "rpc-port-only"
    expression = "destination.port == 8090"
  }

}

resource "google_vertex_ai_reasoning_engine" "runtime" {

  display_name    = local.production ? "Nori Go Agent Runtime" : "Nori Sandbox Go Agent Runtime"
  region          = local.region
  deletion_policy = "PREVENT"
  spec {

    service_account = google_service_account.runtime.email
    identity_type   = "SERVICE_ACCOUNT"
    agent_framework = "custom"
    container_spec {
      image_uri = var.runtime_image
      port      = 8080
    }

    deployment_spec {

      min_instances = local.runtime_settings.minInstances
      max_instances = local.runtime_settings.maxInstances
      # Streams hold slots until their next heartbeat notices the browser left.
      container_concurrency = local.runtime_settings.containerConcurrency
      resource_limits       = local.runtime_settings.resourceLimits

      env {
        name  = "NORI_PROJECT_ID"
        value = local.project
      }

      env {
        name  = "MODEL_LOCATION"
        value = "global"
      }

      env {
        name  = "FIRESTORE_DATABASE_ID"
        value = local.database
      }

      env {
        name  = "ARTIFACT_BUCKET"
        value = google_storage_bucket.artifacts.name
      }



      env {
        name  = "MODEL_NAME"
        value = var.model
      }

      dynamic "env" {
        for_each = merge({
          NORI_WORKER_SERVICES            = length(var.worker_services) > 0 ? jsonencode(var.worker_services) : ""
          NORI_WORKER_WAKE_QUEUE          = var.worker_wake_queue
          NORI_AGENT_MODEL                = var.outer_model
          NORI_WAKE_QUEUE                 = google_cloud_tasks_queue.conversation.id
          NORI_WAKE_SERVICE_ACCOUNT       = google_service_account.conversation_wake.email
          NORI_RUNTIME_URL                = var.runtime_invoke_root
          TEMPORAL_ADDRESS                = "quickstart-nori.qtjow.tmprl.cloud:7233"
          TEMPORAL_NAMESPACE              = "quickstart-nori.qtjow"
          TEMPORAL_TASK_QUEUE             = local.task_queue
          TEMPORAL_DEPLOYMENT_NAME        = local.temporal_deployment
          TEMPORAL_API_KEY_SECRET_VERSION = "projects/${local.project}/secrets/nori-temporal-api-key/versions/latest"
        }, local.telemetry_env)
        content {
          name  = env.key
          value = env.value
        }
      }

      dynamic "secret_env" {
        for_each = merge(local.conversation_secrets, local.arize_secret_env)
        content {
          name = secret_env.key
          secret_ref {
            secret  = secret_env.value
            version = secret_env.key == "ARIZE_API_KEY" ? var.observability.arize_secret_version : "latest"
          }
        }
      }

    }

  }

  lifecycle {
    ignore_changes = [spec[0].container_spec[0].image_uri]
  }

  depends_on = [google_project_service.api, google_project_iam_member.runtime_firestore, google_project_iam_member.runtime_model, google_artifact_registry_repository_iam_member.runtime_image_reader, google_artifact_registry_repository_iam_member.platform_image_reader, google_secret_manager_secret_iam_member.conversation_runtime_secrets, google_secret_manager_secret_iam_member.conversation_deploy_secrets, google_secret_manager_secret_iam_member.arize_runtime, google_secret_manager_secret_iam_member.arize_platform]

}

resource "google_cloud_run_v2_service" "web" {

  name                 = local.web_name
  location             = local.region
  deletion_protection  = true
  iap_enabled          = true
  invoker_iam_disabled = false
  ingress              = "INGRESS_TRAFFIC_ALL"
  scaling {
    min_instance_count = 0
    max_instance_count = 1
  }
  template {

    service_account                  = google_service_account.web.email
    timeout                          = "180s"
    max_instance_request_concurrency = 10
    scaling {
      min_instance_count = 0
      max_instance_count = 1
    }

    containers {

      image = var.web_image
      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu = "1", memory = "512Mi"
        }

        cpu_idle = true
      }

      env {
        name  = "NORI_RUNTIME_URL"
        value = local.runtime_url
      }

      env {
        name  = "IAP_AUDIENCE"
        value = "/projects/${local.number}/locations/${local.region}/services/${local.web_name}"
      }
      env {
        name  = "OWNER_EMAIL"
        value = "austinbarwickdavis@gmail.com"
      }

      dynamic "env" {
        for_each = local.web_telemetry_env
        content {
          name  = env.key
          value = env.value
        }
      }

    }

  }

  lifecycle {
    ignore_changes = [template[0].containers[0].image, client, client_version]
  }

  depends_on = [google_project_service.api]

}

# Owner-only website/history invocation grants are configured separately.
# Resume and pause preserve those grants; durable control gates task execution.
output "web_url" {
  value = google_cloud_run_v2_service.web.uri
}

output "runtime_name" {
  value = local.runtime_name
}

output "runtime_url" {
  value = local.runtime_url
}

output "artifact_bucket" {
  value = google_storage_bucket.artifacts.name
}

output "firestore_database" {
  value = local.database
}

output "query_role" {
  value = google_project_iam_custom_role.query.name
}

output "web_service_account" {
  value = google_service_account.web.email
}

output "runtime_service_account" {
  value = google_service_account.runtime.email
}

output "conversation_queue" {
  value = google_cloud_tasks_queue.conversation.id
}

output "conversation_wake_service_account" {
  value = google_service_account.conversation_wake.email
}

output "outer_model" {
  value = var.outer_model
}

output "computer_service_account" {
  value = google_service_account.computer.email
}

output "image_repository" {
  value = "${local.region}-docker.pkg.dev/${local.project}/nori-poc"
}

# Create this native Google-managed identity before applying the grant:
# gcloud beta services identity create --service=aiplatform.googleapis.com
resource "google_artifact_registry_repository_iam_member" "runtime_image_reader" {
  count      = local.production ? 1 : 0
  location   = local.region
  repository = google_artifact_registry_repository.images[0].name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:service-${local.number}@gcp-sa-aiplatform-re.iam.gserviceaccount.com"
}

# Both native service agents need read access for the BYOC image pull.
resource "google_artifact_registry_repository_iam_member" "platform_image_reader" {
  count      = local.production ? 1 : 0
  location   = local.region
  repository = google_artifact_registry_repository.images[0].name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:service-${local.number}@gcp-sa-aiplatform.iam.gserviceaccount.com"
}

output "environment" {
  value = var.environment
}

output "computer_instance" {
  value = google_compute_instance.computer.name
}

output "temporal_deployment" {
  value = local.temporal_deployment
}

output "task_queue" {
  value = local.task_queue
}

# Production retains ownership of shared project resources. These moves only
# change Terraform addresses, not remote resources; sandbox never owns them.
moved {
  from = google_artifact_registry_repository.images
  to   = google_artifact_registry_repository.images[0]
}
moved {
  from = google_project_iam_member.cloudtasks_service_agent
  to   = google_project_iam_member.cloudtasks_service_agent[0]
}
moved {
  from = google_artifact_registry_repository_iam_member.runtime_image_reader
  to   = google_artifact_registry_repository_iam_member.runtime_image_reader[0]
}
moved {
  from = google_artifact_registry_repository_iam_member.platform_image_reader
  to   = google_artifact_registry_repository_iam_member.platform_image_reader[0]
}

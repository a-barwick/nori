variable "request_worker_images" {
  description = "Immutable request-worker build => image digest. Existing pool builds remain retained separately."
  type        = map(string)
  default     = {}
  validation {
    condition = alltrue([for build, image in var.request_worker_images :
      can(regex("^[a-z0-9][a-z0-9-]{0,39}$", build)) && can(regex("^us-central1-docker\\.pkg\\.dev/bot-111549/nori-poc/.+@sha256:[a-f0-9]{64}$", image)) && !contains(keys(var.worker_images), build)
    ])
    error_message = "Use a new immutable request-worker build and image digest; never replace a retained pool build."
  }
}

variable "request_worker_targets" {
  description = "Retained native routing for each service build. Production versions share nori-worker; isolated candidates keep a separate deployment, task queue and wake queue."
  type = map(object({
    deployment_name     = string
    task_queue          = string
    wake_queue_name     = string
    poll_window_seconds = optional(number, 240)
    firestore_database  = optional(string, "nori-poc")
  }))
  default = {}
  validation {
    condition     = toset(keys(var.request_worker_targets)) == toset(keys(var.request_worker_images))
    error_message = "Every request-worker image must have exactly one retained routing target."
  }
  validation {
    condition = alltrue([for target in values(var.request_worker_targets) :
      can(regex("^[a-z0-9][a-z0-9-]{0,127}$", target.deployment_name)) &&
      can(regex("^[a-z0-9][a-z0-9-]{0,127}$", target.task_queue)) &&
      can(regex("^nori-worker(-[a-z0-9][a-z0-9-]{0,50})?$", target.wake_queue_name)) &&
      target.wake_queue_name != "nori-worker-candidate-events" &&
      target.poll_window_seconds >= 1 && target.poll_window_seconds <= 240 && floor(target.poll_window_seconds) == target.poll_window_seconds &&
      ((target.deployment_name == "nori-task" && target.task_queue == "nori-tasks" && target.wake_queue_name == "nori-worker" && target.poll_window_seconds == 240 && target.firestore_database == "nori-poc") ||
      (target.deployment_name != "nori-task" && target.task_queue != "nori-tasks" && target.wake_queue_name != "nori-worker" && target.firestore_database == "nori-worker-candidate"))
    ])
    error_message = "Use DNS-safe names and poll=1..240; production keeps poll=240/database=nori-poc, and candidates isolate all routing names with database=nori-worker-candidate."
  }
  validation {
    condition = alltrue(flatten([for a in values(var.request_worker_targets) : [for b in values(var.request_worker_targets) :
      (a.deployment_name != b.deployment_name && a.task_queue != b.task_queue && a.wake_queue_name != b.wake_queue_name) || (a.deployment_name == b.deployment_name && a.task_queue == b.task_queue && a.wake_queue_name == b.wake_queue_name && a.firestore_database == b.firestore_database)
    ]]))
    error_message = "Builds must share their complete deployment/task-queue/wake-queue target, or isolate all three names."
  }
}

locals {
  # The documented deterministic URL's DNS segment remains <=63 characters
  # even with a 40-character build ID. The explicit audience also accepts it.
  request_worker_urls = { for build, image in var.request_worker_images : build => "https://nori-task-${build}-8273801370.${local.region}.run.app" }
  request_worker_routes = { for build, target in var.request_worker_targets : build => {
    for other, url in local.request_worker_urls : other => url if var.request_worker_targets[other].deployment_name == target.deployment_name
  } }
  request_worker_queues = toset([for target in values(var.request_worker_targets) : target.wake_queue_name])
  production_worker_services = { for build, url in local.request_worker_urls : build => url
    if var.request_worker_targets[build].deployment_name == "nori-task"
  }
  request_wake_caller        = "nori-conversation-wake@${local.project}.iam.gserviceaccount.com"
  candidate_database_enabled = anytrue([for target in values(var.request_worker_targets) : target.firestore_database == "nori-worker-candidate"])
}

# Private validation owns a separate database in this existing state. Production
# nori-poc and its IAM remain exclusively owned by the POC state.
resource "google_firestore_database" "candidate" {
  count                   = local.candidate_database_enabled ? 1 : 0
  project                 = local.project
  name                    = "nori-worker-candidate"
  location_id             = "us-central1"
  type                    = "FIRESTORE_NATIVE"
  delete_protection_state = "DELETE_PROTECTION_ENABLED"
  deletion_policy         = "PREVENT"
  lifecycle { prevent_destroy = true }
}

resource "google_project_iam_member" "candidate_firestore" {
  count   = local.candidate_database_enabled ? 1 : 0
  project = local.project
  role    = "roles/datastore.user"
  member  = "serviceAccount:${local.runner}"
  condition {
    title      = "nori-worker-candidate-state-only"
    expression = "resource.name == 'projects/${local.project}/databases/nori-worker-candidate' || resource.name.startsWith('projects/${local.project}/databases/nori-worker-candidate/') || resource.name == 'projects/8273801370/databases/nori-worker-candidate' || resource.name.startsWith('projects/8273801370/databases/nori-worker-candidate/')"
  }
}

# Candidate Notify exercises native enqueue into this fenced queue. The local
# candidate API consumes its database through LocalLoop; these requests are never
# dispatched to the inherited production runtime URL or production queue.
resource "google_cloud_tasks_queue" "candidate_events" {
  count           = local.candidate_database_enabled ? 1 : 0
  name            = "nori-worker-candidate-events"
  location        = local.region
  desired_state   = "PAUSED"
  deletion_policy = "PREVENT"
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
  lifecycle { prevent_destroy = true }
}

resource "google_cloud_tasks_queue_iam_member" "candidate_events_enqueue" {
  count    = local.candidate_database_enabled ? 1 : 0
  project  = local.project
  location = local.region
  name     = google_cloud_tasks_queue.candidate_events[0].name
  role     = "roles/cloudtasks.enqueuer"
  member   = "serviceAccount:${local.runner}"
}

resource "google_cloud_tasks_queue" "worker" {
  for_each = local.request_worker_queues
  name     = each.key
  location = local.region
  # Apply never opens execution admission. The operator unpause command owns it.
  desired_state   = "PAUSED"
  deletion_policy = "PREVENT"
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
  lifecycle {
    prevent_destroy = true
    ignore_changes  = [desired_state]
  }
}

resource "google_cloud_tasks_queue_iam_member" "worker_enqueue" {
  for_each = google_cloud_tasks_queue.worker
  project  = local.project
  location = local.region
  name     = each.value.name
  role     = "roles/cloudtasks.enqueuer"
  member   = "serviceAccount:${local.runner}"
}

resource "google_cloud_run_v2_service" "worker" {
  for_each             = var.request_worker_images
  name                 = "nori-task-${each.key}"
  location             = local.region
  deletion_protection  = true
  ingress              = "INGRESS_TRAFFIC_ALL"
  invoker_iam_disabled = false
  custom_audiences     = [local.request_worker_urls[each.key]]
  scaling {
    min_instance_count = 0
    max_instance_count = 1
  }
  template {
    service_account                  = local.runner
    max_instance_request_concurrency = 1
    timeout                          = "660s"
    containers {
      image = each.value
      ports { container_port = 8080 }
      resources {
        limits   = { cpu = "1", memory = "512Mi" }
        cpu_idle = true
      }
      dynamic "env" {
        for_each = merge(local.worker_env, {
          FIRESTORE_DATABASE_ID    = var.request_worker_targets[each.key].firestore_database
          NORI_WAKE_QUEUE          = var.request_worker_targets[each.key].firestore_database == "nori-worker-candidate" ? google_cloud_tasks_queue.candidate_events[0].id : local.worker_env.NORI_WAKE_QUEUE
          TEMPORAL_BUILD_ID        = each.key
          TEMPORAL_DEPLOYMENT_NAME = var.request_worker_targets[each.key].deployment_name
          TEMPORAL_TASK_QUEUE      = var.request_worker_targets[each.key].task_queue
          NORI_WORKER_SERVICES     = jsonencode(local.request_worker_routes[each.key])
          NORI_WORKER_WAKE_QUEUE   = google_cloud_tasks_queue.worker[var.request_worker_targets[each.key].wake_queue_name].id
          NORI_WORKER_POLL_WINDOW  = "${var.request_worker_targets[each.key].poll_window_seconds}s"
          NORI_WORKER_IDLE_WINDOW  = "15s"
        })
        content {
          name  = env.key
          value = env.value
        }
      }
      env {
        name = "TEMPORAL_API_KEY"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.temporal_api_key.secret_id
            version = "latest"
          }
        }
      }
    }
  }
  lifecycle { prevent_destroy = true }
  depends_on = [google_secret_manager_secret_iam_member.runner_key, google_cloud_tasks_queue_iam_member.worker_enqueue, google_project_iam_member.candidate_firestore, google_firestore_database.candidate, google_cloud_tasks_queue_iam_member.candidate_events_enqueue]
}

resource "google_cloud_run_v2_service_iam_member" "worker_invoke" {
  for_each = google_cloud_run_v2_service.worker
  project  = local.project
  location = local.region
  name     = each.value.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${local.request_wake_caller}"
}

output "request_worker_services" { value = local.request_worker_urls }
output "request_worker_targets" { value = var.request_worker_targets }
output "production_worker_services" { value = local.production_worker_services }
output "worker_wake_queues" { value = { for name, queue in google_cloud_tasks_queue.worker : name => queue.id } }
output "worker_wake_queue" { value = try(google_cloud_tasks_queue.worker["nori-worker"].id, "") }

output "candidate_firestore_database" { value = try(google_firestore_database.candidate[0].name, "") }
output "candidate_event_queue" { value = try(google_cloud_tasks_queue.candidate_events[0].id, "") }

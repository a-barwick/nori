# This is a separate bootstrap state. It does not own POC or Temporal resources.
# Do not apply until the GitHub connection and state migration are reviewed.
terraform {
  backend "local" {}
  required_version = ">= 1.10"
  required_providers {
    google = { source = "hashicorp/google", version = "8.8.1" }
  }
}
provider "google" {
  project = "bot-111549"
  region  = "us-central1"
}
variable "repository" {
  description = "Existing Cloud Build v2 GitHub repository resource in us-central1"
  type        = string
  validation {
    condition     = can(regex("^projects/bot-111549/locations/us-central1/connections/[^/]+/repositories/[^/]+$", var.repository))
    error_message = "Pass the existing regional Cloud Build v2 repository resource name."
  }
}
variable "release_tools_image" {
  description = "Reviewed release-tools image built and published once during setup, by immutable digest"
  type        = string
  validation {
    condition     = can(regex("^us-central1-docker\\.pkg\\.dev/bot-111549/nori-poc/release-tools@sha256:[a-f0-9]{64}$", var.release_tools_image))
    error_message = "Use the immutable release-tools digest in the existing Nori registry."
  }
}
variable "enable_triggers" {
  description = "Enable only after separate live setup, state, and first-deployment review"
  type        = bool
  default     = false
}
variable "sentry_source_maps" {
  description = "Optional existing same-project source-map token secret and public Sentry organization/project slugs."
  type = object({
    secret       = string
    version      = optional(string, "latest")
    organization = string
    project      = string
  })
  default = null
  validation {
    condition = var.sentry_source_maps == null ? true : (
      can(regex("^projects/bot-111549/secrets/[A-Za-z0-9_-]+$", var.sentry_source_maps.secret)) &&
      can(regex("^(latest|[1-9][0-9]*)$", var.sentry_source_maps.version)) &&
      can(regex("^[A-Za-z0-9_-]+$", var.sentry_source_maps.organization)) &&
      can(regex("^[A-Za-z0-9_-]+$", var.sentry_source_maps.project))
    )
    error_message = "Use an existing project secret/version and Sentry organization/project slugs."
  }
}
locals {
  project = "bot-111549"
  region  = "us-central1"
  bucket  = "bot-111549-nori-poc"
  accounts = {
    check   = "nori-ci-check"
    prepare = "nori-ci-prepare"
    deploy  = "nori-ci-deploy"
    sandbox = "nori-ci-sandbox"
  }
}
resource "google_service_account" "ci" {
  for_each     = local.accounts
  account_id   = each.value
  display_name = "Nori CI ${each.key}"
}
resource "google_secret_manager_secret_iam_member" "sentry_source_maps" {
  for_each  = var.sentry_source_maps == null ? toset([]) : toset([var.sentry_source_maps.secret])
  secret_id = each.value
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.ci["prepare"].email}"
}
resource "google_project_iam_member" "logs" {
  for_each = local.accounts
  project  = local.project
  role     = "roles/logging.logWriter"
  member   = "serviceAccount:${google_service_account.ci[each.key].email}"
}
# Existing repository is owned by the POC state. These are additive grants only.
resource "google_artifact_registry_repository_iam_member" "prepare" {
  project    = local.project
  location   = local.region
  repository = "nori-poc"
  role       = "roles/artifactregistry.writer"
  member     = "serviceAccount:${google_service_account.ci["prepare"].email}"
}
resource "google_artifact_registry_repository_iam_member" "deploy" {
  project    = local.project
  location   = local.region
  repository = "nori-poc"
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${google_service_account.ci["deploy"].email}"
}
resource "google_storage_bucket_iam_member" "prepare_objects" {
  bucket = local.bucket
  role   = "roles/storage.objectCreator"
  member = "serviceAccount:${google_service_account.ci["prepare"].email}"
  condition {
    title      = "nori-candidate-objects-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/candidates/') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/computer/')"
  }
}
# A dedicated bucket is essential: GCS Terraform locking and object versioning
# protect both existing state histories without taking ownership of the POC bucket.
resource "google_storage_bucket" "state" {
  name                        = "bot-111549-nori-ci-state"
  location                    = "US-CENTRAL1"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false
  versioning { enabled = true }
  lifecycle { prevent_destroy = true }
}
resource "google_storage_bucket_iam_member" "state_deploy" {
  bucket = google_storage_bucket.state.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "production-state-only"
    expression = "resource.name.startsWith('projects/_/buckets/${google_storage_bucket.state.name}/objects/nori/poc/') || resource.name.startsWith('projects/_/buckets/${google_storage_bucket.state.name}/objects/nori/temporal/')"
  }
}
# Object listing is a bucket-level permission. Restrict object reads/writes to
# the release namespaces, while allowing listing for candidate discovery.
resource "google_project_iam_custom_role" "release_list" {
  role_id     = "noriCiReleaseList"
  title       = "Nori CI release object listing"
  permissions = ["storage.objects.list"]
}
resource "google_storage_bucket_iam_member" "release_list" {
  for_each = toset(["prepare", "deploy", "sandbox"])
  bucket   = local.bucket
  role     = google_project_iam_custom_role.release_list.name
  member   = "serviceAccount:${google_service_account.ci[each.key].email}"
}
resource "google_storage_bucket_iam_member" "release_journal" {
  bucket = local.bucket
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "nori-release-journal-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/deployments/production/')"
  }
}
resource "google_storage_bucket_iam_member" "release_read" {
  bucket = local.bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "nori-release-inputs-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/candidates/') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/computer/') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/releases/computer-agent-') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/validated/')"
  }
}
resource "google_storage_bucket_iam_member" "published_computer" {
  bucket = local.bucket
  role   = "roles/storage.objectCreator"
  member = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "nori-published-computer-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/releases/computer-agent-')"
  }
}
resource "google_project_iam_custom_role" "deploy" {
  role_id = "noriCiDeploy"
  title   = "Nori CI deployment operations"
  permissions = [
    "cloudbuild.builds.get",
    "aiplatform.reasoningEngines.get", "aiplatform.reasoningEngines.update", "aiplatform.reasoningEngines.query", "aiplatform.operations.get",
    "run.services.get", "run.services.create", "run.services.update", "run.services.getIamPolicy", "run.services.setIamPolicy", "run.operations.get", "run.revisions.get",
    "run.workerpools.list", "run.workerpools.get", "run.workerpools.create", "run.workerpools.update", "run.workerpools.getIamPolicy", "run.workerpools.setIamPolicy",
    "compute.instances.get", "compute.zoneOperations.get",
    # Temporal-state refresh, including the optional billing monitor resources.
    "resourcemanager.projects.get", "resourcemanager.projects.getIamPolicy",
    "serviceusage.services.get", "serviceusage.services.list", "serviceusage.services.use",
    "iam.serviceAccounts.get", "iam.serviceAccounts.getIamPolicy", "iam.roles.get",
    "secretmanager.secrets.get", "secretmanager.secrets.getIamPolicy",
    "run.jobs.get", "run.jobs.getIamPolicy",
    "cloudscheduler.jobs.get", "monitoring.metricDescriptors.get",
    "monitoring.notificationChannels.get", "monitoring.alertPolicies.get"
  ]
}
resource "google_project_iam_member" "deploy" {
  project = local.project
  role    = google_project_iam_custom_role.deploy.name
  member  = "serviceAccount:${google_service_account.ci["deploy"].email}"
}
resource "google_service_account_iam_member" "deploy_act_as" {
  for_each           = toset(["nori-poc-runtime", "nori-poc-web", "nori-poc-computer", "nori-conversation-wake"])
  service_account_id = "projects/${local.project}/serviceAccounts/${each.key}@${local.project}.iam.gserviceaccount.com"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.ci["deploy"].email}"
}
resource "google_secret_manager_secret_iam_member" "temporal" {
  project   = local.project
  secret_id = "nori-temporal-api-key"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.ci["deploy"].email}"
}
resource "google_cloudbuild_trigger" "check" {
  disabled        = !var.enable_triggers
  project         = local.project
  location        = local.region
  name            = "nori-pr-check"
  service_account = google_service_account.ci["check"].id
  filename        = "core/infrastructure/release/cloudbuild-pr.yaml"
  repository_event_config {
    repository = var.repository
    pull_request {
      branch          = "^main$"
      comment_control = "COMMENTS_ENABLED_FOR_EXTERNAL_CONTRIBUTORS_ONLY"
    }
  }
}
resource "google_cloudbuild_trigger" "prepare" {
  disabled        = !var.enable_triggers
  project         = local.project
  location        = local.region
  name            = "nori-main-prepare"
  service_account = google_service_account.ci["prepare"].id
  filename        = "core/infrastructure/release/cloudbuild-prepare.yaml"
  repository_event_config {
    repository = var.repository
    push { branch = "^main$" }
  }
  substitutions = {
    _SENTRY_TOKEN_SECRET  = var.sentry_source_maps == null ? "" : var.sentry_source_maps.secret
    _SENTRY_TOKEN_VERSION = var.sentry_source_maps == null ? "latest" : var.sentry_source_maps.version
    _SENTRY_ORG           = var.sentry_source_maps == null ? "" : var.sentry_source_maps.organization
    _SENTRY_PROJECT       = var.sentry_source_maps == null ? "" : var.sentry_source_maps.project
  }
  depends_on = [google_secret_manager_secret_iam_member.sentry_source_maps]
}
resource "google_cloudbuild_trigger" "release" {
  disabled        = !var.enable_triggers
  project         = local.project
  location        = local.region
  name            = "nori-release"
  service_account = google_service_account.ci["deploy"].id
  filename        = "core/infrastructure/release/cloudbuild-release.yaml"
  repository_event_config {
    repository = var.repository
    push { tag = "^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$" }
  }
  substitutions = { _TOOLS_IMAGE = var.release_tools_image }
}
resource "google_pubsub_topic" "prepared" {
  name = "nori-prepared-builds"
}
resource "google_pubsub_topic_iam_member" "prepare_publish" {
  topic  = google_pubsub_topic.prepared.name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${google_service_account.ci["prepare"].email}"
}
resource "google_cloudbuild_trigger" "sandbox" {
  disabled        = !var.enable_triggers
  project         = local.project
  location        = local.region
  name            = "nori-sandbox"
  service_account = google_service_account.ci["sandbox"].id
  pubsub_config { topic = google_pubsub_topic.prepared.id }
  source_to_build {
    repository = var.repository
    ref        = "refs/heads/main"
    repo_type  = "GITHUB"
  }
  git_file_source {
    repository = var.repository
    path       = "core/infrastructure/release/cloudbuild-sandbox.yaml"
    revision   = "refs/heads/main"
    repo_type  = "GITHUB"
  }
  substitutions = {
    _TOOLS_IMAGE      = var.release_tools_image
    _SOURCE_COMMIT    = "$(body.message.data.substitutions.COMMIT_SHA)"
    _PREPARE_BUILD_ID = "$(body.message.data.id)"
    _PREPARE_STATUS   = "$(body.message.data.status)"
    _PREPARE_TRIGGER  = "$(body.message.data.buildTriggerId)"
  }
  filter = "_PREPARE_STATUS == 'SUCCESS' && _PREPARE_TRIGGER == '${google_cloudbuild_trigger.prepare.trigger_id}'"
}
output "prepare_trigger_id" { value = google_cloudbuild_trigger.prepare.trigger_id }
output "state_bucket" { value = google_storage_bucket.state.name }
# Service-level, additive IAP grant for authenticated smoke requests.
resource "google_iap_web_cloud_run_service_iam_member" "smoke" {
  project                = local.project
  location               = local.region
  cloud_run_service_name = "nori-web"
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = "serviceAccount:${google_service_account.ci["deploy"].email}"
}
# The deploy identity signs its own IAP smoke JWT; it cannot impersonate other
# service accounts through this grant.
resource "google_service_account_iam_member" "smoke_sign" {
  service_account_id = google_service_account.ci["deploy"].name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.ci["deploy"].email}"
}
resource "google_storage_bucket_iam_member" "prepare_computer_read" {
  bucket = local.bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.ci["prepare"].email}"
  condition {
    title      = "nori-prepared-computer-read-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/computer/')"
  }
}

# Compute Engine supports resource-name IAM conditions; this grant cannot alter
# metadata or stop any VM other than the retained computer.
resource "google_project_iam_custom_role" "computer_deploy" {
  role_id     = "noriCiComputerDeploy"
  title       = "Nori CI retained computer operations"
  permissions = ["compute.instances.setMetadata", "compute.instances.stop"]
}
resource "google_project_iam_member" "computer_deploy" {
  project = local.project
  role    = google_project_iam_custom_role.computer_deploy.name
  member  = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "nori-retained-computer-only"
    expression = "resource.name == 'projects/bot-111549/zones/us-central1-a/instances/nori-computer'"
  }
}

# Worker service create/update and IAM operations require trusted project-level
# CI permissions. The release runner checks the saved plan before apply; retained
# services may only gain the new route, never change image or other configuration.
resource "google_project_iam_custom_role" "web_deploy" {
  role_id     = "noriCiWebDeploy"
  title       = "Nori CI web deployment"
  permissions = ["run.services.update"]
}
resource "google_cloud_run_v2_service_iam_member" "web_deploy" {
  project  = local.project
  location = local.region
  name     = "nori-web"
  role     = google_project_iam_custom_role.web_deploy.name
  member   = "serviceAccount:${google_service_account.ci["deploy"].email}"
}

# Match the existing runtime's database condition; the deploy identity can
# read task evidence and update pause control only in the Nori database.
resource "google_project_iam_custom_role" "firestore" {
  role_id     = "noriCiFirestore"
  title       = "Nori CI state access"
  permissions = ["datastore.entities.get", "datastore.entities.list", "datastore.entities.update"]
}
resource "google_project_iam_member" "firestore" {
  project = local.project
  role    = google_project_iam_custom_role.firestore.name
  member  = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "nori-state-only"
    expression = "resource.name == 'projects/bot-111549/databases/nori-poc' || resource.name.startsWith('projects/bot-111549/databases/nori-poc/') || resource.name == 'projects/8273801370/databases/nori-poc' || resource.name.startsWith('projects/8273801370/databases/nori-poc/')"
  }
}

# Bootstrap these identities only after the isolated application stack exists.
variable "sandbox_runtime_name" {
  description = "Reviewed sandbox POC runtime_name output; grants access only on that engine"
  type        = string
  validation {
    condition     = can(regex("^projects/8273801370/locations/us-central1/reasoningEngines/[0-9]+$", var.sandbox_runtime_name))
    error_message = "Supply the sandbox runtime_name output after separate setup."
  }
}
resource "google_artifact_registry_repository_iam_member" "sandbox" {
  project    = local.project
  location   = local.region
  repository = "nori-poc"
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_storage_bucket_iam_member" "state_sandbox" {
  bucket = google_storage_bucket.state.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "sandbox-state-only"
    expression = "resource.name.startsWith('projects/_/buckets/${google_storage_bucket.state.name}/objects/nori/sandbox/')"
  }
}
resource "google_storage_bucket_iam_member" "state_list" {
  for_each = toset(["deploy", "sandbox"])
  bucket   = google_storage_bucket.state.name
  role     = google_project_iam_custom_role.release_list.name
  member   = "serviceAccount:${google_service_account.ci[each.key].email}"
}
resource "google_storage_bucket_iam_member" "sandbox_journal" {
  bucket = local.bucket
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "sandbox-journal-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/deployments/sandbox/')"
  }
}
resource "google_storage_bucket_iam_member" "sandbox_receipt" {
  bucket = local.bucket
  role   = "roles/storage.objectCreator"
  member = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "sandbox-promotion-evidence-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/validated/')"
  }
}
resource "google_storage_bucket_iam_member" "sandbox_inputs" {
  bucket = local.bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "prepared-inputs-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/candidates/') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/computer/')"
  }
}
resource "google_storage_bucket_iam_member" "sandbox_computer" {
  for_each = toset(["roles/storage.objectCreator", "roles/storage.objectViewer"])
  bucket   = "bot-111549-nori-sandbox"
  role     = each.key
  member   = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "sandbox-computer-only"
    expression = "resource.name.startsWith('projects/_/buckets/bot-111549-nori-sandbox/objects/releases/computer-agent-')"
  }
}
resource "google_storage_bucket_iam_member" "sandbox_computer_list" {
  bucket = "bot-111549-nori-sandbox"
  role   = google_project_iam_custom_role.release_list.name
  member = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
# Cloud Run service/pool administration requires trusted project-level CI
# permissions. The sandbox runner validates its saved plan and uses isolated
# worker state. This is not a project security boundary.
resource "google_project_iam_custom_role" "sandbox" {
  role_id     = "noriCiSandbox"
  title       = "Nori sandbox CI operations"
  permissions = setsubtract(google_project_iam_custom_role.deploy.permissions, ["aiplatform.reasoningEngines.update", "aiplatform.reasoningEngines.query"])
}
resource "google_project_iam_member" "sandbox" {
  project = local.project
  role    = google_project_iam_custom_role.sandbox.name
  member  = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_project_iam_custom_role" "sandbox_runtime" {
  role_id     = "noriCiSandboxRuntime"
  title       = "Nori sandbox runtime deployment and smoke"
  permissions = ["aiplatform.reasoningEngines.get", "aiplatform.reasoningEngines.update", "aiplatform.reasoningEngines.query"]
}
resource "google_vertex_ai_reasoning_engine_iam_member" "sandbox_runtime" {
  project          = local.project
  region           = local.region
  reasoning_engine = var.sandbox_runtime_name
  role             = google_project_iam_custom_role.sandbox_runtime.name
  member           = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_service_account_iam_member" "sandbox_act_as" {
  for_each           = toset(["nori-sandbox-runtime", "nori-sandbox-web", "nori-sandbox-computer", "nori-sandbox-conversation-wake"])
  service_account_id = "projects/${local.project}/serviceAccounts/${each.key}@${local.project}.iam.gserviceaccount.com"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_secret_manager_secret_iam_member" "sandbox_temporal" {
  project   = local.project
  secret_id = "nori-temporal-api-key"
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_iap_web_cloud_run_service_iam_member" "sandbox_smoke" {
  project                = local.project
  location               = local.region
  cloud_run_service_name = "nori-sandbox-web"
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_service_account_iam_member" "sandbox_sign" {
  service_account_id = google_service_account.ci["sandbox"].name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_project_iam_member" "sandbox_computer" {
  project = local.project
  role    = google_project_iam_custom_role.computer_deploy.name
  member  = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "sandbox-computer-only"
    expression = "resource.name == 'projects/bot-111549/zones/us-central1-a/instances/nori-sandbox-computer'"
  }
}
resource "google_cloud_run_v2_service_iam_member" "sandbox_web" {
  project  = local.project
  location = local.region
  name     = "nori-sandbox-web"
  role     = google_project_iam_custom_role.web_deploy.name
  member   = "serviceAccount:${google_service_account.ci["sandbox"].email}"
}
resource "google_project_iam_member" "sandbox_firestore" {
  project = local.project
  role    = google_project_iam_custom_role.firestore.name
  member  = "serviceAccount:${google_service_account.ci["sandbox"].email}"
  condition {
    title      = "sandbox-state-only"
    expression = "resource.name == 'projects/bot-111549/databases/nori-sandbox' || resource.name.startsWith('projects/bot-111549/databases/nori-sandbox/') || resource.name == 'projects/8273801370/databases/nori-sandbox' || resource.name.startsWith('projects/8273801370/databases/nori-sandbox/')"
  }
}

# Queue creation and IAM are setup-owned. CI only dispatches one recorded warm
# request and uses the existing pause/resume/drain commands on retained queues.
resource "google_project_iam_custom_role" "worker_queue" {
  role_id     = "noriCiWorkerQueue"
  title       = "Nori CI worker queue fencing and registration"
  permissions = ["cloudtasks.queues.get", "cloudtasks.queues.getIamPolicy", "cloudtasks.queues.pause", "cloudtasks.queues.resume", "cloudtasks.tasks.list", "cloudtasks.tasks.get", "cloudtasks.tasks.create", "cloudtasks.tasks.run"]
}
resource "google_cloud_tasks_queue_iam_member" "worker_queue" {
  for_each = {
    nori-worker                  = "deploy"
    nori-worker-candidate        = "deploy"
    nori-worker-candidate-events = "deploy"
    nori-sandbox-worker          = "sandbox"
  }
  project  = local.project
  location = "us-west1"
  name     = each.key
  role     = google_project_iam_custom_role.worker_queue.name
  member   = "serviceAccount:${google_service_account.ci[each.value].email}"
}

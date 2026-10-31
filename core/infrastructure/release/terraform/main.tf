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
locals {
  project = "bot-111549"
  region  = "us-central1"
  bucket  = "bot-111549-nori-poc"
  accounts = {
    check   = "nori-ci-check"
    prepare = "nori-ci-prepare"
    deploy  = "nori-ci-deploy"
  }
}
resource "google_service_account" "ci" {
  for_each     = local.accounts
  account_id   = each.value
  display_name = "Nori CI ${each.key}"
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
}
# Object listing is a bucket-level permission. Restrict object reads/writes to
# the release namespaces, while allowing listing for candidate discovery.
resource "google_project_iam_custom_role" "release_list" {
  role_id     = "noriCiReleaseList"
  title       = "Nori CI release object listing"
  permissions = ["storage.objects.list"]
}
resource "google_storage_bucket_iam_member" "release_list" {
  for_each = toset(["prepare", "deploy"])
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
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/deployments/')"
  }
}
resource "google_storage_bucket_iam_member" "release_read" {
  bucket = local.bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.ci["deploy"].email}"
  condition {
    title      = "nori-release-inputs-only"
    expression = "resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/candidates/') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/computer/') || resource.name.startsWith('projects/_/buckets/${local.bucket}/objects/releases/computer-agent-')"
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
    "run.services.get", "run.operations.get", "run.revisions.get",
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
  for_each           = toset(["nori-poc-runtime", "nori-poc-web", "nori-poc-computer"])
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
  project         = local.project
  location        = local.region
  name            = "nori-main-prepare"
  service_account = google_service_account.ci["prepare"].id
  filename        = "core/infrastructure/release/cloudbuild-prepare.yaml"
  repository_event_config {
    repository = var.repository
    push { branch = "^main$" }
  }
}
resource "google_cloudbuild_trigger" "manual" {
  project         = local.project
  location        = local.region
  name            = "nori-release"
  service_account = google_service_account.ci["deploy"].id
  source_to_build {
    repository = var.repository
    ref        = "refs/heads/main"
    repo_type  = "GITHUB"
  }
  git_file_source {
    repository = var.repository
    path       = "core/infrastructure/release/cloudbuild-release.yaml"
    revision   = "refs/heads/main"
    repo_type  = "GITHUB"
  }
  substitutions = { _TOOLS_IMAGE = var.release_tools_image }
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

# Cloud Run service IAM can scope deployment to this existing service. Worker
# pool create/update and IAM operations remain project-scoped: pools are new
# during release and Cloud Run administrative resource-name conditions are not
# supported. The release runner validates the saved Terraform plan before apply.
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

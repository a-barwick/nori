# Service accounts and the project-level roles they hold. Resource-level grants
# sit next to the resource they apply to.

resource "google_service_account" "nori_runtime" {
  account_id   = "nori-runtime"
  display_name = "Nori Core Runtime"
}

resource "google_service_account" "nori_web" {
  account_id   = "nori-web"
  display_name = "Nori Web Frontend"
}

resource "google_service_account" "nori_web_mac" {
  account_id   = "nori-web-mac"
  display_name = "Nori Mac Web"
}

resource "google_service_account" "nori_home_ai_relay" {
  account_id   = "nori-home-ai-relay"
  display_name = "Nori Home AI relay"
}

resource "google_service_account" "nori_site" {
  account_id   = "nori-site"
  display_name = "Nori public site runtime"
  description  = "Runs the nori-site Cloud Run service. No roles."
}

resource "google_service_account" "nori_ci_images" {
  account_id   = "nori-ci-images"
  display_name = "Nori CI image builds"
}

resource "google_service_account" "nori_ci_release" {
  account_id   = "nori-ci-release"
  display_name = "Nori CI releases"
}

resource "google_service_account" "nori_ci_infra" {
  account_id   = "nori-ci-infra"
  display_name = "Nori CI infrastructure"
  description  = "Runs terraform plan and apply for infra/ from GitHub Actions."
}

locals {
  project_roles = {
    "runtime-sql"         = { member = google_service_account.nori_runtime.member, role = "roles/cloudsql.client" }
    "runtime-secrets"     = { member = google_service_account.nori_runtime.member, role = "roles/secretmanager.secretAccessor" }
    "web-secrets"         = { member = google_service_account.nori_web.member, role = "roles/secretmanager.secretAccessor" }
    "release-run"         = { member = google_service_account.nori_ci_release.member, role = "roles/run.developer" }
    "release-logs"        = { member = google_service_account.nori_ci_release.member, role = "roles/logging.viewer" }
    "infra-viewer"        = { member = google_service_account.nori_ci_infra.member, role = "roles/viewer" }
    "infra-run"           = { member = google_service_account.nori_ci_infra.member, role = "roles/run.admin" }
    "infra-sa-admin"      = { member = google_service_account.nori_ci_infra.member, role = "roles/iam.serviceAccountAdmin" }
    "infra-sa-user"       = { member = google_service_account.nori_ci_infra.member, role = "roles/iam.serviceAccountUser" }
    "infra-role-admin"    = { member = google_service_account.nori_ci_infra.member, role = "roles/iam.roleAdmin" }
    "infra-wif-admin"     = { member = google_service_account.nori_ci_infra.member, role = "roles/iam.workloadIdentityPoolAdmin" }
    "infra-project-iam"   = { member = google_service_account.nori_ci_infra.member, role = "roles/resourcemanager.projectIamAdmin" }
    "infra-secrets"       = { member = google_service_account.nori_ci_infra.member, role = "roles/secretmanager.admin" }
    "infra-kms"           = { member = google_service_account.nori_ci_infra.member, role = "roles/cloudkms.admin" }
    "infra-storage"       = { member = google_service_account.nori_ci_infra.member, role = "roles/storage.admin" }
    "infra-registry"      = { member = google_service_account.nori_ci_infra.member, role = "roles/artifactregistry.admin" }
    "infra-sql"           = { member = google_service_account.nori_ci_infra.member, role = "roles/cloudsql.admin" }
    "infra-lb"            = { member = google_service_account.nori_ci_infra.member, role = "roles/compute.loadBalancerAdmin" }
    "infra-armor"         = { member = google_service_account.nori_ci_infra.member, role = "roles/compute.securityAdmin" }
    "infra-iap"           = { member = google_service_account.nori_ci_infra.member, role = "roles/iap.admin" }
    "infra-service-usage" = { member = google_service_account.nori_ci_infra.member, role = "roles/serviceusage.serviceUsageAdmin" }
  }
}

resource "google_project_iam_member" "this" {
  for_each = local.project_roles

  project = local.project
  role    = each.value.role
  member  = each.value.member
}

# The relay may connect only to nori-pg.
resource "google_project_iam_member" "relay_sql" {
  project = local.project
  role    = "roles/cloudsql.client"
  member  = google_service_account.nori_home_ai_relay.member

  condition {
    title      = "NoriHomeAICloudSQLInstance"
    expression = "resource.name == 'projects/${local.project}/instances/${google_sql_database_instance.nori_pg.name}' && resource.service == 'sqladmin.googleapis.com'"
  }
}

# Release deploys revisions that run as these accounts.
resource "google_service_account_iam_member" "release_acts_as" {
  for_each = {
    runtime = google_service_account.nori_runtime.name
    web     = google_service_account.nori_web.name
    web_mac = google_service_account.nori_web_mac.name
    site    = google_service_account.nori_site.name
  }

  service_account_id = each.value
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.nori_ci_release.member
}

resource "google_project_iam_custom_role" "mac_iap_policy_reader" {
  role_id     = "noriMacIapPolicyReader"
  title       = "Read Nori IAP access policy"
  permissions = ["iap.webServices.getIamPolicy"]
}

# GitHub Actions authenticates through Workload Identity Federation. The provider
# accepts only this repository's main branch.
resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github"
  display_name              = "GitHub Actions"
}

resource "google_iam_workload_identity_pool_provider" "nori_main" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "nori-main"
  display_name                       = "${local.github_repo} main"
  attribute_condition                = "assertion.repository=='${local.github_repo}' && assertion.ref=='refs/heads/main'"
  attribute_mapping = {
    "attribute.ref"        = "assertion.ref"
    "attribute.repository" = "assertion.repository"
    "google.subject"       = "assertion.sub"
  }

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

resource "google_service_account_iam_member" "github_impersonation" {
  for_each = {
    images  = google_service_account.nori_ci_images.name
    release = google_service_account.nori_ci_release.name
    infra   = google_service_account.nori_ci_infra.name
  }

  service_account_id = each.value
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${local.github_repo}"
}

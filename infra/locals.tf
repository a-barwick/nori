locals {
  project        = "bot-111549"
  project_number = "8273801370"
  region         = "us-central1"
  github_repo    = "a-barwick/nori"
  registry       = "${local.region}-docker.pkg.dev/${local.project}/${google_artifact_registry_repository.nori_poc.repository_id}"
  cloud_sql      = "${local.project}:${local.region}:${google_sql_database_instance.nori_pg.name}"
  kms_key        = google_kms_crypto_key.connected_accounts.id
  core_url       = "https://nori-core-${local.project_number}.${local.region}.run.app"
  web_origin     = "https://app.noribot.chat"

  # Values that would leak personal data in this public repository live in the
  # nori-terraform-private secret as a JSON object. See secrets.tf. They are not
  # credentials, and marking them sensitive would hide every env diff on Core and
  # Worker, so CI prints a plan summary instead of the full diff.
  private = jsondecode(nonsensitive(data.google_secret_manager_secret_version.terraform_private.secret_data))
}

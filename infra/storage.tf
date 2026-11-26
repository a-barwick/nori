# Artifact storage, container images, encryption keys and Terraform state.

resource "google_storage_bucket" "artifacts" {
  name                        = "${local.project}-nori-artifacts"
  location                    = "US-CENTRAL1"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  lifecycle_rule {
    action {
      type = "AbortIncompleteMultipartUpload"
    }
    condition {
      age = 1
    }
  }

  lifecycle_rule {
    action {
      type = "Delete"
    }
    condition {
      days_since_noncurrent_time = 7
      with_state                 = "ARCHIVED"
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_storage_bucket_iam_member" "artifacts_runtime" {
  bucket = google_storage_bucket.artifacts.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.nori_runtime.member
}

resource "google_storage_bucket" "tfstate" {
  name                        = "${local.project}-nori-tfstate"
  location                    = "US-CENTRAL1"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_artifact_registry_repository" "nori_poc" {
  repository_id = "nori-poc"
  location      = local.region
  format        = "DOCKER"
}

resource "google_artifact_registry_repository_iam_member" "this" {
  for_each = {
    images  = { role = "roles/artifactregistry.writer", member = google_service_account.nori_ci_images.member }
    release = { role = "roles/artifactregistry.reader", member = google_service_account.nori_ci_release.member }
  }

  location   = google_artifact_registry_repository.nori_poc.location
  repository = google_artifact_registry_repository.nori_poc.name
  role       = each.value.role
  member     = each.value.member
}

resource "google_kms_key_ring" "credentials" {
  name     = "nori-credentials"
  location = local.region
}

# Wraps the data keys for per-user credentials and artifacts (NORI_CREDENTIAL_KMS_KEY).
resource "google_kms_crypto_key" "connected_accounts" {
  name            = "connected-accounts"
  key_ring        = google_kms_key_ring.credentials.id
  rotation_period = "7776000s"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_kms_crypto_key_iam_member" "connected_accounts_runtime" {
  crypto_key_id = google_kms_crypto_key.connected_accounts.id
  role          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  member        = google_service_account.nori_runtime.member
}

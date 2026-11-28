# Only the private tester sync writes this state. An empty roster removes the
# beta grants while preserving access granted to the same people independently.
terraform {
  required_version = ">= 1.9"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.46"
    }
  }
  backend "gcs" {
    bucket = "bot-111549-nori-tfstate"
    prefix = "private-beta"
  }
}

provider "google" {
  project                         = "bot-111549"
  region                          = "us-central1"
  add_terraform_attribution_label = false
}

variable "tester_emails" {
  type      = set(string)
  sensitive = true
  nullable  = false
  validation {
    condition     = alltrue([for email in var.tester_emails : can(regex("^[a-z0-9+_.-]+@[a-z0-9.-]+[.][a-z0-9-]+$", email))])
    error_message = "Each tester needs a Google sign-in email."
  }
}

# Infrastructure creates version 1 before mounting the secret on Core.
data "google_secret_manager_secret_version" "bootstrap" {
  secret  = "nori-private-beta-emails"
  version = "1"
}

resource "google_secret_manager_secret_version" "roster" {
  depends_on  = [data.google_secret_manager_secret_version.bootstrap]
  secret      = "projects/bot-111549/secrets/nori-private-beta-emails"
  secret_data = jsonencode(sort(tolist(var.tester_emails)))
  lifecycle {
    create_before_destroy = true
  }
}

resource "google_iap_web_cloud_run_service_iam_binding" "testers" {
  count                  = nonsensitive(length(var.tester_emails)) > 0 ? 1 : 0
  location               = "us-central1"
  cloud_run_service_name = "nori-web"
  role                   = "roles/iap.httpsResourceAccessor"
  members                = [for email in var.tester_emails : "user:${email}"]
  condition {
    title      = "NoriPrivateBeta"
    expression = "true"
  }
}

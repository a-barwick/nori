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
    prefix = "prod"
  }
}

provider "google" {
  project = local.project
  region  = local.region

  # The attribution label would change every adopted resource, and it forces
  # replacement of Cloud Run domain mappings.
  add_terraform_attribution_label = false
}

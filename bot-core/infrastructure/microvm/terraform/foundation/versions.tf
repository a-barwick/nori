terraform {
  required_version = ">= 1.14.6, < 2.0.0"
  backend "local" {}
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.8.1"
    }
  }
}

provider "google" {
  project                         = "bot-111549"
  region                          = "us-central1"
  zone                            = "us-central1-a"
  billing_project                 = "bot-111549"
  user_project_override           = true
  add_terraform_attribution_label = false
}

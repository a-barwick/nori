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
  zone    = "us-central1-a"
}

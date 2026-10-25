locals {
  apis = toset([
    "compute.googleapis.com",
    "billingbudgets.googleapis.com",
    "cloudasset.googleapis.com",
    "iap.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "serviceusage.googleapis.com",
  ])
}

resource "google_project_service" "api" {
  for_each           = local.apis
  project            = "bot-111549"
  service            = each.value
  disable_on_destroy = false
}

resource "google_billing_budget" "monthly" {
  billing_account = "010FFA-6C77F1-18FF6D"
  display_name    = "Bot $50 monthly cost alerts"

  budget_filter {
    projects               = ["projects/8273801370"]
    calendar_period        = "MONTH"
    credit_types_treatment = "EXCLUDE_ALL_CREDITS"
  }
  amount {
    specified_amount {
      currency_code = "USD"
      units         = "50"
    }
  }
  dynamic "threshold_rules" {
    for_each = [0.1, 0.5, 0.8, 1.0]
    content {
      threshold_percent = threshold_rules.value
      spend_basis       = "CURRENT_SPEND"
    }
  }
  threshold_rules {
    threshold_percent = 1.0
    spend_basis       = "FORECASTED_SPEND"
  }

  # An absent all_updates_rule preserves the existing default IAM recipients.
  lifecycle {
    prevent_destroy = true
  }
  depends_on = [google_project_service.api]
}

resource "google_compute_disk" "computer_data" {
  project = "bot-111549"
  name    = "bot-computer-data"
  zone    = "us-central1-a"
  type    = "pd-standard"
  size    = 20
  labels = {
    purpose = "bot-persistent-computer"
  }

  lifecycle {
    prevent_destroy = true
    postcondition {
      condition     = self.disk_id == "3757334953750232874"
      error_message = "The retained disk identity changed. Stop and investigate; never replace or initialize it."
    }
  }
  depends_on = [google_project_service.api]
}

resource "google_compute_network" "computer" {
  project                 = "bot-111549"
  name                    = "nori-computer"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"
  depends_on              = [google_project_service.api]
}

resource "google_compute_subnetwork" "computer" {
  project       = "bot-111549"
  name          = "nori-computer"
  region        = "us-central1"
  ip_cidr_range = "10.233.0.0/24"
  network       = google_compute_network.computer.id
}

resource "google_compute_firewall" "iap_ssh" {
  project       = "bot-111549"
  name          = "nori-computer-iap-ssh"
  network       = google_compute_network.computer.id
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["nori-computer"]
  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# Apply separately during reviewed sandbox setup. Shared APIs, registry, secrets,
# and production state are prerequisites, never resources owned by this root.
resource "google_compute_network" "computer" {
  name                    = "nori-sandbox-computer"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"
  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_subnetwork" "computer" {
  name          = "nori-sandbox-computer"
  region        = "us-central1"
  ip_cidr_range = "10.234.0.0/24"
  network       = google_compute_network.computer.id
  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_firewall" "iap_ssh" {
  name          = "nori-sandbox-computer-iap-ssh"
  network       = google_compute_network.computer.id
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["nori-sandbox-computer"]
  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# New blank retained storage. No production snapshot, data copy, format command,
# or guest initialization is performed here. Initialize the guest separately.
resource "google_compute_disk" "computer_data" {
  name = "nori-sandbox-computer-data"
  zone = "us-central1-a"
  type = "pd-standard"
  size = 20
  labels = {
    purpose     = "nori-sandbox-computer"
    environment = "sandbox"
  }
  lifecycle {
    prevent_destroy = true
  }
}

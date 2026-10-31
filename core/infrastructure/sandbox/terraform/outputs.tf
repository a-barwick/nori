output "environment" {
  value = "sandbox"
}

output "network_id" {
  value = google_compute_network.computer.id
}

output "subnetwork_id" {
  value = google_compute_subnetwork.computer.id
}

output "retained_disk_id" {
  value = google_compute_disk.computer_data.id
}

output "retained_disk_numeric_id" {
  value = google_compute_disk.computer_data.disk_id
}

output "computer_instance" {
  value = "nori-sandbox-computer"
}

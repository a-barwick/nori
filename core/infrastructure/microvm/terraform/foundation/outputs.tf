output "project_id" {
  value = "bot-111549"
}
output "zone" {
  value = "us-central1-a"
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

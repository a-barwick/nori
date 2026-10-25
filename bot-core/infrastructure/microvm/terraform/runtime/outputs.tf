output "host_name" {
  value = one(google_compute_instance.host[*].name)
}
output "termination_time" {
  value = var.enabled ? var.termination_time : null
}

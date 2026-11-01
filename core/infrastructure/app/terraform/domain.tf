resource "google_cloud_run_domain_mapping" "web" {
  count           = local.production ? 1 : 0
  name            = "noribot.chat"
  project         = local.project
  location        = local.region
  deletion_policy = "PREVENT"

  metadata {
    namespace = local.project
  }

  spec {
    # Map the existing service without changing its IAP or invocation policy.
    route_name       = "nori-web"
    certificate_mode = "AUTOMATIC"
  }
}

output "web_custom_url" {
  value = local.production ? "https://${google_cloud_run_domain_mapping.web[0].name}" : null
}

output "web_domain_dns_records" {
  value = local.production ? google_cloud_run_domain_mapping.web[0].status[0].resource_records : []
}

output "web_domain_conditions" {
  value = local.production ? google_cloud_run_domain_mapping.web[0].status[0].conditions : []
}

moved {
  from = google_cloud_run_domain_mapping.web
  to   = google_cloud_run_domain_mapping.web[0]
}

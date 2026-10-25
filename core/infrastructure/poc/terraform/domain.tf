resource "google_cloud_run_domain_mapping" "web" {
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
  value = "https://${google_cloud_run_domain_mapping.web.name}"
}

output "web_domain_dns_records" {
  value = google_cloud_run_domain_mapping.web.status[0].resource_records
}

output "web_domain_conditions" {
  value = google_cloud_run_domain_mapping.web.status[0].conditions
}

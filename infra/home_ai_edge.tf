# Public HTTPS edge for the Home AI relay at relay.noribot.chat. The relay only
# accepts traffic from this load balancer, and Cloud Armor throttles each client
# IP.

resource "google_compute_global_address" "home_ai_edge" {
  name = "nori-home-ai-edge"
}

resource "google_compute_managed_ssl_certificate" "home_ai_edge" {
  name = "nori-home-ai-edge"

  managed {
    domains = ["relay.noribot.chat"]
  }
}

resource "google_compute_region_network_endpoint_group" "home_ai_edge" {
  name                  = "nori-home-ai-edge"
  region                = local.region
  network_endpoint_type = "SERVERLESS"

  cloud_run {
    service = google_cloud_run_v2_service.nori_connector_relay.name
  }
}

resource "google_compute_security_policy" "home_ai_throttle" {
  name = "nori-home-ai-throttle"
  type = "CLOUD_ARMOR"

  rule {
    action   = "throttle"
    priority = 1000

    match {
      versioned_expr = "SRC_IPS_V1"
      config {
        src_ip_ranges = ["*"]
      }
    }

    rate_limit_options {
      conform_action = "allow"
      exceed_action  = "deny(429)"
      enforce_on_key = "IP"

      rate_limit_threshold {
        count        = 60
        interval_sec = 60
      }
    }
  }

  rule {
    description = "default rule"
    action      = "allow"
    priority    = 2147483647

    match {
      versioned_expr = "SRC_IPS_V1"
      config {
        src_ip_ranges = ["*"]
      }
    }
  }
}

resource "google_compute_backend_service" "home_ai_edge" {
  name                  = "nori-home-ai-edge"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  protocol              = "HTTP"
  security_policy       = google_compute_security_policy.home_ai_throttle.self_link
  # Serverless NEGs do not drain connections.
  connection_draining_timeout_sec = 0

  backend {
    group = google_compute_region_network_endpoint_group.home_ai_edge.id
  }
}

resource "google_compute_url_map" "home_ai_edge" {
  name            = "nori-home-ai-edge"
  default_service = google_compute_backend_service.home_ai_edge.id
}

resource "google_compute_target_https_proxy" "home_ai_edge" {
  name             = "nori-home-ai-edge"
  url_map          = google_compute_url_map.home_ai_edge.id
  ssl_certificates = [google_compute_managed_ssl_certificate.home_ai_edge.id]
}

resource "google_compute_global_forwarding_rule" "home_ai_edge" {
  name                  = "nori-home-ai-edge"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  ip_address            = google_compute_global_address.home_ai_edge.address
  port_range            = "443"
  target                = google_compute_target_https_proxy.home_ai_edge.id
}

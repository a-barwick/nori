# Optional native daily billing monitor. No image, secret payload, or notification
# resource is invented when this configuration is absent.
variable "spend_alert" {
  description = "Enable only after reviewing an immutable alert image and an existing billing-key Secret Manager reference. This is an alert, not a spend cap."
  type = object({
    image          = string
    billing_secret = string
    secret_version = optional(string, "latest")
    threshold_usd  = optional(number, 10)
    cost_unit      = optional(string, "cents")
    schedule       = optional(string, "0 17 * * *")
    email          = optional(string, "austinbarwickdavis@gmail.com")
  })
  default = null
  validation {
    condition = var.spend_alert == null ? true : (
      can(regex("^us-central1-docker\\.pkg\\.dev/bot-111549/nori-poc/.+@sha256:[a-f0-9]{64}$", var.spend_alert.image)) &&
      can(regex("^projects/bot-111549/secrets/[A-Za-z0-9_-]+$", var.spend_alert.billing_secret)) &&
      can(regex("^(latest|[1-9][0-9]*)$", var.spend_alert.secret_version)) &&
      var.spend_alert.threshold_usd > 0 && contains(["cents", "dollars"], var.spend_alert.cost_unit) &&
      var.spend_alert.email == "austinbarwickdavis@gmail.com"
    )
    error_message = "Use a reviewed sha256 image in the existing registry, an existing project Secret Manager reference/version, positive USD threshold, documented cost unit, and Austin's configured email."
  }
}

locals {
  spend_alert = var.spend_alert == null ? {} : { daily = var.spend_alert }
  spend_metrics = var.spend_alert == null ? {} : {
    month_to_date_usd = { unit = "{USD}", description = "Reported current-month Temporal account ContractedCost, converted to USD. Provisional and delayed; not an invoice total or cap." }
    threshold_reached = { unit = "1", description = "1 when a successful report reaches the configured month-to-date USD threshold; otherwise 0." }
    check_ok          = { unit = "1", description = "1 only after a complete, interpretable billing report; 0 for errors or unknown charge data. Unknown is never zero spend." }
  }
}

resource "google_project_service" "spend_alert_api" {
  for_each           = var.spend_alert == null ? toset([]) : toset(["run.googleapis.com", "cloudscheduler.googleapis.com", "monitoring.googleapis.com"])
  project            = local.project
  service            = each.value
  disable_on_destroy = false
}

resource "google_service_account" "spend_alert" {
  for_each     = local.spend_alert
  account_id   = "nori-temporal-billing"
  display_name = "Read Temporal billing and publish monitoring metrics"
}

resource "google_service_account" "spend_scheduler" {
  for_each     = local.spend_alert
  account_id   = "nori-temporal-billing-schedule"
  display_name = "Invoke only the Temporal billing check job"
}

resource "google_secret_manager_secret_iam_member" "spend_alert_key" {
  for_each  = local.spend_alert
  secret_id = each.value.billing_secret
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.spend_alert[each.key].email}"
}

resource "google_project_iam_member" "spend_metric_writer" {
  for_each = local.spend_alert
  project  = local.project
  role     = "roles/monitoring.metricWriter"
  member   = "serviceAccount:${google_service_account.spend_alert[each.key].email}"
}

resource "google_monitoring_metric_descriptor" "spend_alert" {
  for_each     = local.spend_metrics
  project      = local.project
  type         = "custom.googleapis.com/nori/temporal_billing/${each.key}"
  display_name = "Temporal billing ${each.key}"
  metric_kind  = "GAUGE"
  value_type   = "DOUBLE"
  unit         = each.value.unit
  description  = each.value.description
  depends_on   = [google_project_service.spend_alert_api]
}

resource "google_cloud_run_v2_job" "spend_alert" {
  for_each            = local.spend_alert
  project             = local.project
  name                = "nori-temporal-billing"
  location            = local.region
  deletion_protection = true
  template {
    task_count  = 1
    parallelism = 1
    template {
      service_account = google_service_account.spend_alert[each.key].email
      timeout         = "360s"
      max_retries     = 0
      containers {
        image = each.value.image
        args  = ["--publish", "--threshold-usd", tostring(each.value.threshold_usd), "--cost-unit", each.value.cost_unit, "--project", local.project]
        resources {
          limits = { cpu = "1", memory = "512Mi" }
        }
        env {
          name = "TEMPORAL_BILLING_API_KEY"
          value_source {
            secret_key_ref {
              secret  = each.value.billing_secret
              version = each.value.secret_version
            }
          }
        }
      }
    }
  }
  depends_on = [google_project_service.spend_alert_api, google_secret_manager_secret_iam_member.spend_alert_key, google_project_iam_member.spend_metric_writer, google_monitoring_metric_descriptor.spend_alert]
}

resource "google_cloud_run_v2_job_iam_member" "spend_scheduler" {
  for_each = local.spend_alert
  project  = local.project
  location = local.region
  name     = google_cloud_run_v2_job.spend_alert[each.key].name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.spend_scheduler[each.key].email}"
}

resource "google_cloud_scheduler_job" "spend_alert" {
  for_each         = local.spend_alert
  project          = local.project
  name             = "nori-temporal-billing-daily"
  region           = local.region
  description      = "Read delayed Temporal account charges once daily; never wake task workers or the computer"
  schedule         = each.value.schedule
  time_zone        = "Etc/UTC"
  attempt_deadline = "30s"
  # Native retryCount/maxRetryDuration defaults are zero: no retries.
  # A zero-only block is omitted by the API and causes perpetual provider drift.
  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/${google_cloud_run_v2_job.spend_alert[each.key].id}:run"
    body        = base64encode("{}")
    headers     = { "Content-Type" = "application/json" }
    oauth_token {
      service_account_email = google_service_account.spend_scheduler[each.key].email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }
  depends_on = [google_cloud_run_v2_job_iam_member.spend_scheduler]
}

resource "google_monitoring_notification_channel" "spend_email" {
  for_each     = local.spend_alert
  project      = local.project
  display_name = "Austin Temporal billing alerts"
  type         = "email"
  labels       = { email_address = each.value.email }
  depends_on   = [google_project_service.spend_alert_api]
}

resource "google_monitoring_alert_policy" "spend_threshold" {
  for_each              = local.spend_alert
  project               = local.project
  display_name          = format("Temporal reported monthly charges reached $%s USD", each.value.threshold_usd)
  combiner              = "OR"
  notification_channels = [google_monitoring_notification_channel.spend_email[each.key].name]
  documentation {
    mime_type = "text/markdown"
    content   = "The daily Temporal account report reached the configured USD threshold. This includes all reported charge categories/namespaces, not GCP spend. Current-month charges are provisional and lag at least 24 hours, rounded to daily granularity. This alert does not stop compute or cap charges. Inspect the billing Job's JSON logs for report ID, generation time, month, actual reported total, and unit basis."
  }
  conditions {
    display_name = "Successful billing check reached configured USD threshold"
    condition_threshold {
      filter          = "metric.type = \"custom.googleapis.com/nori/temporal_billing/threshold_reached\" AND resource.type = \"global\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0.5
      duration        = "0s"
      aggregations {
        alignment_period   = "86400s"
        per_series_aligner = "ALIGN_MAX"
      }
    }
  }
  depends_on = [google_monitoring_metric_descriptor.spend_alert]
}

resource "google_monitoring_alert_policy" "spend_check_failed" {
  for_each              = local.spend_alert
  project               = local.project
  display_name          = "Temporal billing check failed or returned unknown charges"
  combiner              = "OR"
  notification_channels = [google_monitoring_notification_channel.spend_email[each.key].name]
  documentation {
    mime_type = "text/markdown"
    content   = "Temporal spend is unknown. Inspect the Cloud Run Job and Scheduler. An empty or failed provider report never publishes zero spend. Deployment acceptance must verify the schedule, initial metric, and email channel; this policy cannot detect a Job that never runs or cannot publish any metrics. No paid task worker or computer needs to be resumed to inspect billing."
  }
  conditions {
    display_name = "Billing report failed or had unknown charge data"
    condition_threshold {
      filter          = "metric.type = \"custom.googleapis.com/nori/temporal_billing/check_ok\" AND resource.type = \"global\""
      comparison      = "COMPARISON_LT"
      threshold_value = 0.5
      duration        = "0s"
      aggregations {
        alignment_period   = "86400s"
        per_series_aligner = "ALIGN_MIN"
      }
    }
  }
  depends_on = [google_monitoring_metric_descriptor.spend_alert]
}

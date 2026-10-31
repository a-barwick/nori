# Telemetry values here are public identifiers and an existing secret reference,
# never credential payloads. Empty defaults preserve existing deployments.
variable "observability" {
  type = object({
    core_dsn                = optional(string, "")
    web_dsn                 = optional(string, "")
    arize_space_id          = optional(string, "")
    arize_project_name      = optional(string, "nori")
    arize_project_url       = optional(string, "")
    arize_secret            = optional(string, "")
    arize_secret_version    = optional(string, "latest")
    arize_endpoint          = optional(string, "")
    arize_capture_content   = optional(bool, false)
    trace_sample_rate       = optional(number, 0.05)
    sentry_errors_per_hour  = optional(number, 50)
    sentry_logs_per_hour    = optional(number, 100)
    sentry_metrics_per_hour = optional(number, 200)
  })
  default = {}
  validation {
    condition = (((var.observability.arize_secret == "" && var.observability.arize_space_id == "") ||
      (can(regex("^projects/bot-111549/secrets/[A-Za-z0-9_-]+$", var.observability.arize_secret)) && var.observability.arize_space_id != "")) &&
      can(regex("^(latest|[1-9][0-9]*)$", var.observability.arize_secret_version)) &&
      (var.observability.arize_endpoint == "" || startswith(var.observability.arize_endpoint, "https://")) &&
      var.observability.trace_sample_rate >= 0 && var.observability.trace_sample_rate <= 1 &&
    alltrue([for n in [var.observability.sentry_errors_per_hour, var.observability.sentry_logs_per_hour, var.observability.sentry_metrics_per_hour] : n >= 0 && n <= 10000 && floor(n) == n]))
    error_message = "Use a paired AX space ID and existing same-project secret reference, a valid version/HTTPS endpoint, sampling 0..1, and whole hourly limits 0..10000."
  }
}

locals {
  telemetry_enabled = var.observability.core_dsn != "" || var.observability.arize_secret != ""
  telemetry_env_values = local.telemetry_enabled ? merge({
    SENTRY_DSN                   = var.observability.core_dsn
    SENTRY_ENVIRONMENT           = var.environment
    SENTRY_TRACES_SAMPLE_RATE    = tostring(var.observability.trace_sample_rate)
    NORI_SENTRY_ERRORS_PER_HOUR  = tostring(var.observability.sentry_errors_per_hour)
    NORI_SENTRY_LOGS_PER_HOUR    = tostring(var.observability.sentry_logs_per_hour)
    NORI_SENTRY_METRICS_PER_HOUR = tostring(var.observability.sentry_metrics_per_hour)
    }, var.observability.arize_secret == "" ? {} : {
    ARIZE_SPACE_ID             = var.observability.arize_space_id
    ARIZE_PROJECT_NAME         = var.observability.arize_project_name
    ARIZE_PROJECT_URL          = var.observability.arize_project_url
    ARIZE_OTLP_ENDPOINT        = var.observability.arize_endpoint
    NORI_ARIZE_CAPTURE_CONTENT = tostring(var.observability.arize_capture_content)
  }) : {}
  # Agent Runtime rejects environment entries with an empty value. Omit
  # optional settings so the SDK can use its own defaults.
  telemetry_env = { for name, value in local.telemetry_env_values : name => value if value != "" }
  web_telemetry_env = var.observability.web_dsn == "" ? {} : {
    SENTRY_WEB_DSN            = var.observability.web_dsn
    SENTRY_ENVIRONMENT        = var.environment
    SENTRY_TRACES_SAMPLE_RATE = tostring(var.observability.trace_sample_rate)
  }
  arize_secret_env = var.observability.arize_secret == "" ? {} : { ARIZE_API_KEY = basename(var.observability.arize_secret) }
}

# This POC state owns the runtime identity's binding. The Temporal worker uses
# the same identity, avoiding duplicate IAM ownership in the worker state.
resource "google_secret_manager_secret_iam_member" "arize_runtime" {
  for_each  = var.observability.arize_secret == "" ? toset([]) : toset([var.observability.arize_secret])
  secret_id = each.value
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.runtime.email}"
}

# The shared Agent Platform service agent binding has one production owner;
# sandbox reuses it when both environments use this existing secret.
resource "google_secret_manager_secret_iam_member" "arize_platform" {
  for_each  = local.production && var.observability.arize_secret != "" ? toset([var.observability.arize_secret]) : toset([])
  secret_id = each.value
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:service-${local.number}@gcp-sa-aiplatform.iam.gserviceaccount.com"
}

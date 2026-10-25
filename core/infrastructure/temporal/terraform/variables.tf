variable "worker_images" {
  description = "Immutable build ID => existing Artifact Registry image digest. Retain old builds while Pinned Workflows reference them."
  type        = map(string)
  default     = {}
  validation {
    condition = alltrue([for build, image in var.worker_images :
      can(regex("^[a-z0-9][a-z0-9-]{0,39}$", build)) && can(regex("^us-central1-docker\\.pkg\\.dev/bot-111549/nori-poc/.+@sha256:[a-f0-9]{64}$", image))
    ])
    error_message = "Use a lowercase DNS-safe build ID up to 40 characters and a sha256 digest in the existing nori-poc registry."
  }
}

variable "active_build_id" {
  description = "The only build WCI may scale during this POC. Other retained versions have no WCI grant."
  type        = string
  default     = null
  nullable    = true
}

variable "session" {
  description = "Explicit authorized session, never an unattended start. All times RFC3339 UTC; shutdown_at must be minute-aligned. Operator must bound hard_deadline_at by 22:00 America/Los_Angeles."
  type = object({
    started_at        = string
    access_expires_at = string
    shutdown_at       = string
    hard_deadline_at  = string
  })
  default  = null
  nullable = true
  validation {
    condition = var.session == null ? true : try(
      alltrue([for value in values(var.session) : can(regex("Z$", value))]) &&
      timecmp(var.session.access_expires_at, var.session.started_at) > 0 &&
      timecmp(timeadd(var.session.access_expires_at, "120s"), var.session.shutdown_at) <= 0 &&
      timecmp(timeadd(var.session.shutdown_at, "600s"), var.session.hard_deadline_at) <= 0 &&
      timecmp(var.session.hard_deadline_at, timeadd(var.session.started_at, "3h")) <= 0 &&
    formatdate("ss", var.session.shutdown_at) == "00", false)
    error_message = "Session must be <=3h, with WCI denial >=2m before minute-aligned shutdown and shutdown >=10m before hard deadline."
  }
}

variable "shutdown_verified" {
  description = "Set true only after live proof that Scheduler's OAuth identity patches every retained pool to zero, and WCI access expires first."
  type        = bool
  default     = false
}

variable "wci_enabled" {
  description = "Keep false until the cloud-side shutdown path is live-proven. This also controls whether the invoker service account is disabled."
  type        = bool
  default     = false
}

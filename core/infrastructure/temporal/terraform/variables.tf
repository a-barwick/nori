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

variable "scalable_build_ids" {
  description = "Retained builds compatible with pause-only control. Legacy session-expiry workers must remain excluded."
  type        = set(string)
  default     = []
  validation {
    condition     = alltrue([for build in var.scalable_build_ids : contains(keys(var.worker_images), build)])
    error_message = "Every scalable build must have a retained immutable worker image."
  }
}

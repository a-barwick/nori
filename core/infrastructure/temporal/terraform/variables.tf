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

variable "runtime_invoke_root" {
  description = "Existing POC runtime_url output ending in /api, used to wake the outer conversation when a task emits an event."
  type        = string
  validation {
    condition     = can(regex("^https://us-central1-aiplatform\\.googleapis\\.com/reasoningEngines/v1/projects/8273801370/locations/us-central1/reasoningEngines/[0-9]+/api$", var.runtime_invoke_root))
    error_message = "Use the existing bot-111549 native Agent Runtime invoke root ending in /api."
  }
}

variable "enabled" {
  type        = bool
  default     = false
  description = "Explicit opt-in to one disposable host. Set false and apply after a run, including after cloud-side deletion."
}

variable "termination_time" {
  type        = string
  default     = ""
  description = "Explicit RFC3339 DELETE deadline, future and within 30 minutes, no later than today's 22:00 America/Los_Angeles."
}

variable "host_name" {
  type        = string
  default     = "nori-computer"
  description = "Disposable GCE host name."
  validation {
    condition     = can(regex("^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$", var.host_name))
    error_message = "Use a valid GCE instance name."
  }
}

variable "public_key_path" {
  type        = string
  default     = "~/.ssh/bot-111549.pub"
  description = "Operator SSH PUBLIC key only; never pass a private key."
}

variable "foundation_state_path" {
  type        = string
  default     = "~/.local/state/nori/bot-111549/terraform/foundation.tfstate"
  description = "Local foundation state produced through tf.sh; contains no cloud credentials."
}

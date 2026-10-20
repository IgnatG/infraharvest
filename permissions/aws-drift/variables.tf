variable "trusted_principal_arns" {
  description = "IAM principals allowed to assume the role, e.g. [\"arn:aws:iam::123456789012:role/platform-ci\"]."
  type        = list(string)

  validation {
    condition     = length(var.trusted_principal_arns) > 0 && alltrue([for arn in var.trusted_principal_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:", arn))])
    error_message = "List at least one IAM principal ARN."
  }
}

variable "state_bucket" {
  description = "The S3 bucket of the roots' state, the backend's bucket."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.state_bucket))
    error_message = "Use an S3 bucket name."
  }
}

variable "state_key_prefix" {
  description = "The key prefix of the roots' state (the backend's key_prefix, see docs/configuration.md); empty for the whole bucket."
  type        = string
  default     = ""
}

variable "state_kms_key_arn" {
  description = "ARN of the KMS key the state is encrypted with (the backend's kms_key_id), or null when it sets none."
  type        = string
  default     = null

  validation {
    condition     = var.state_kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:", var.state_kms_key_arn))
    error_message = "Use a KMS key ARN."
  }
}

variable "role_name" {
  description = "Name of the drift role."
  type        = string
  default     = "infraharvest-drift"
}

variable "max_session_duration" {
  description = "Maximum session length in seconds (1 to 12 hours). A drift run's credentials must stay valid until it ends."
  type        = number
  default     = 3600

  validation {
    condition     = var.max_session_duration >= 3600 && var.max_session_duration <= 43200
    error_message = "Use a value between 3600 and 43200 seconds."
  }
}

variable "region" {
  description = "Region for the AWS provider. IAM is global; any enabled region works."
  type        = string
  default     = "us-east-1"
}

variable "tags" {
  description = "Tags for the role."
  type        = map(string)
  default     = {}
}

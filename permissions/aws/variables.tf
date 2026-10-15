variable "trusted_principal_arns" {
  description = "IAM principals allowed to assume the role, e.g. [\"arn:aws:iam::123456789012:role/platform-ci\"]."
  type        = list(string)

  validation {
    condition     = length(var.trusted_principal_arns) > 0 && alltrue([for arn in var.trusted_principal_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:", arn))])
    error_message = "List at least one IAM principal ARN."
  }
}

variable "role_name" {
  description = "Name of the read-only role."
  type        = string
  default     = "infraharvest-readonly"
}

variable "max_session_duration" {
  description = "Maximum session length in seconds (1 to 12 hours)."
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

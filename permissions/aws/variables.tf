variable "trusted_principal_arns" {
  description = "IAM principals allowed to assume the role, e.g. [\"arn:aws:iam::123456789012:role/platform-ci\"]. List these, github_oidc_subjects, or both."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for arn in var.trusted_principal_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:", arn))])
    error_message = "Use IAM principal ARNs."
  }
}

variable "github_oidc_subjects" {
  description = "GitHub Actions workflows allowed to assume the role through the account's OIDC provider, as patterns of their token's sub claim, e.g. [\"repo:acme/infra:ref:refs/heads/main\"] or [\"repo:acme/infra:*\"]."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for subject in var.github_oidc_subjects : can(regex("^repo:[A-Za-z0-9_.-]+/[^/:]+:.+$", subject))])
    error_message = "Start each subject with repo:<owner>/<repository>: and spell the owner out, e.g. repo:acme/infra:ref:refs/heads/main."
  }
}

variable "create_github_oidc_provider" {
  description = "Whether to create the account's GitHub Actions OIDC provider (token.actions.githubusercontent.com). An account has one: leave false to use the one it has, and create it in one configuration only."
  type        = bool
  default     = false
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

variable "trusted_principal_arns" {
  description = "IAM principals allowed to assume the role, e.g. [\"arn:aws:iam::123456789012:role/platform-ci\"]. Name roles or users: an account's root (arn:aws:iam::<account>:root) is refused. List these, github_oidc_subjects, or both."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for arn in var.trusted_principal_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:", arn))])
    error_message = "Use IAM principal ARNs."
  }

  # An account's root trusts every principal in the account whose own
  # policies let it assume the role, not the account's root user alone.
  validation {
    condition     = alltrue([for arn in var.trusted_principal_arns : !endswith(arn, ":root")])
    error_message = "Name the roles or users that may assume the role, such as arn:aws:iam::123456789012:role/platform-ci, not an account's root, which trusts every principal in the account allowed to assume roles."
  }
}

variable "github_oidc_subjects" {
  description = "GitHub Actions workflows allowed to assume the role through the account's OIDC provider, as their token's sub claim: a branch, tag, environment or pull_request of one repository, e.g. [\"repo:acme/infra:ref:refs/heads/main\", \"repo:acme/infra:environment:production\"]. A repository with immutable subject claims names its owner and itself with their IDs, e.g. \"repo:acme@123456/infra@456789:ref:refs/heads/main\". repo:<owner>/<repository>:* trusts every workflow of that one repository, on any branch."
  type        = list(string)
  default     = []

  # The owner and repository are spelled out, by name or, in GitHub's
  # immutable subjects, by name@ID, and nothing else is a pattern: a
  # wildcard in either would trust other repositories, and one in a branch
  # or environment name more refs than meant.
  validation {
    condition     = alltrue([for subject in var.github_oidc_subjects : can(regex("^repo:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+|[A-Za-z0-9_.-]+@[0-9]+/[A-Za-z0-9_.-]+@[0-9]+):(ref:refs/(heads|tags)/[^*?]+|environment:[^*?]+|pull_request|\\*)$", subject))])
    error_message = "Give each subject as repo:<owner>/<repository>: (or, with immutable subject claims, repo:<owner>@<owner ID>/<repository>@<repository ID>:) and then ref:refs/heads/<branch>, ref:refs/tags/<tag>, environment:<name> or pull_request, without * or ?; or as repo:<owner>/<repository>:* for every workflow of the repository."
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

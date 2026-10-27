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
  description = "GitHub Actions workflows allowed to assume the role through the account's OIDC provider, as their token's sub claim: a branch, tag or environment of one repository, e.g. [\"repo:acme/infra:ref:refs/heads/main\", \"repo:acme/infra:environment:production\"]. A repository with immutable subject claims names its owner and itself with their IDs, e.g. \"repo:acme@123456/infra@456789:ref:refs/heads/main\". The role reads the state, which holds secrets, so pull_request and repo:<owner>/<repository>:*, which a pull request's workflow matches, are refused."
  type        = list(string)
  default     = []

  # The owner and repository are spelled out, by name or, in GitHub's
  # immutable subjects, by name@ID, and nothing else is a pattern: a
  # wildcard in either would trust other repositories, and one in a branch
  # or environment name more refs than meant.
  validation {
    condition     = alltrue([for subject in var.github_oidc_subjects : can(regex("^repo:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+|[A-Za-z0-9_.-]+@[0-9]+/[A-Za-z0-9_.-]+@[0-9]+):(ref:refs/(heads|tags)/[^*?]+|environment:[^*?]+|pull_request|\\*)$", subject))])
    error_message = "Give each subject as repo:<owner>/<repository>: (or, with immutable subject claims, repo:<owner>@<owner ID>/<repository>@<repository ID>:) and then ref:refs/heads/<branch>, ref:refs/tags/<tag> or environment:<name>, without * or ?."
  }

  # A pull request's workflow runs the code it proposes, which anyone who
  # can open one writes: trusting it, or every workflow of the repository,
  # would let that code read the state and its secrets.
  validation {
    condition     = alltrue([for subject in var.github_oidc_subjects : !endswith(subject, ":pull_request") && !endswith(subject, ":*")])
    error_message = "The drift role reads the state, which holds secrets: trust the branch, tag or environment drift runs on (repo:<owner>/<repository>:ref:refs/heads/main, or :environment:<name>), not pull_request or repo:<owner>/<repository>:*, which pull requests' workflows match."
  }
}

variable "create_github_oidc_provider" {
  description = "Whether to create the account's GitHub Actions OIDC provider (token.actions.githubusercontent.com). An account has one: leave false to use the one it has, and create it in one configuration only."
  type        = bool
  default     = false
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

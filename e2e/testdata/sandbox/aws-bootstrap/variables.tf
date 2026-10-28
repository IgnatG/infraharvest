variable "region" {
  description = "The sandbox's region: AWS_REGION. IAM is global; the roles are made through this one."
  type        = string
  default     = "eu-west-2"
}

variable "github_repository" {
  description = "The repository whose sandbox environment signs in as the roles, as owner/name, for the gh commands the outputs print."
  type        = string
  default     = "IgnatG/infraharvest"
}

variable "github_subject" {
  description = "The subject of the GitHub OIDC tokens the roles trust: the sandbox environment's jobs. The repository uses immutable subjects, with the owner's and the repository's IDs."
  type        = string
  default     = "repo:IgnatG@6107088/infraharvest@1420684118:environment:sandbox"

  validation {
    condition     = can(regex("^repo:[A-Za-z0-9_.-]+@[0-9]+/[A-Za-z0-9_.-]+@[0-9]+:environment:[^*?]+$", var.github_subject))
    error_message = "Name one environment of one repository, with the owner's and the repository's IDs and no wildcard: repo:<owner>@<ID>/<repository>@<ID>:environment:<name>."
  }
}

variable "create_github_oidc_provider" {
  description = "Whether to create the account's GitHub Actions OIDC provider. An account has one: leave false when it already has."
  type        = bool
  default     = false
}

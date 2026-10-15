variable "project_id" {
  description = "The sandbox project: the estate is created in it, and the read-only service account reads all of it. Best a project of its own, which holds nothing else."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "Use the project ID, not its name or number."
  }
}

variable "region" {
  description = "Where the estate's regional resources are created: GOOGLE_REGION. The bootstrap itself creates nothing regional."
  type        = string
  default     = "europe-west2"
}

variable "github_repository" {
  description = "The repository whose sandbox environment signs in as the service accounts, as owner/name, for the gh commands the outputs print."
  type        = string
  default     = "IgnatG/infraharvest"
}

variable "github_repository_id" {
  description = "The repository's immutable numeric ID, which the workload identity provider requires: unlike its name, no other repository can take it."
  type        = string
  default     = "1420684118"
}

variable "github_repository_owner_id" {
  description = "The immutable numeric ID of the repository's owner, which the workload identity provider requires."
  type        = string
  default     = "6107088"
}

variable "github_subject" {
  description = "The subject of the GitHub OIDC tokens the service accounts trust: the sandbox environment's jobs. The repository uses immutable subjects, with the owner's and the repository's IDs."
  type        = string
  default     = "repo:IgnatG@6107088/infraharvest@1420684118:environment:sandbox"
}

variable "pool_id" {
  description = "The workload identity pool's ID. A deleted pool keeps its ID for 30 days, so pick another to create the pool again sooner."
  type        = string
  default     = "infraharvest-github"
}

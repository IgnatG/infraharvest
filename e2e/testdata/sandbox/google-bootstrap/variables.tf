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

variable "slot" {
  description = "Which sandbox project this is: \"\" for the first, which every estate uses, or \"second\", where the environments estate (TestSandboxGoogleEnvironments) puts prod. It changes only the names of the variables the gh commands set, GOOGLE_SECOND_ for the second; the region is the first's."
  type        = string
  default     = ""

  validation {
    condition     = contains(["", "second"], var.slot)
    error_message = "Use \"\" for the first project or \"second\"."
  }
}

variable "first_readonly_service_account" {
  description = "With slot = \"second\": the email of the first project's infraharvest-ci-readonly, its bootstrap's readonly_service_account output. It gets what permissions/google grants on this project too, for the environments estate's first import, which reads both projects with one sign-in."
  type        = string
  default     = ""

  validation {
    condition     = var.first_readonly_service_account == "" || can(regex("^[a-z][a-z0-9-]+@[a-z0-9-]+[.]iam[.]gserviceaccount[.]com$", var.first_readonly_service_account))
    error_message = "Use the service account's email."
  }
}

variable "deny_data_reads" {
  description = "Whether the read-only service accounts' data reads are denied in the project, as permissions/google denies them. Managing deny policies takes Deny Admin (roles/iam.denyAdmin), which is granted on the organization: set false for a project outside an organization."
  type        = bool
  default     = true
}

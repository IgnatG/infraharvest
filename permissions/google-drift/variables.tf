variable "project_id" {
  description = "Project infraharvest may read."
  type        = string
}

variable "member" {
  description = "Who runs infraharvest drift, e.g. serviceAccount:infraharvest@my-project.iam.gserviceaccount.com or user:me@example.com."
  type        = string

  validation {
    condition     = can(regex("^(user|serviceAccount|group|principal|principalSet):", var.member))
    error_message = "Use an IAM member such as serviceAccount:<email>."
  }
}

variable "grant_read_only" {
  description = "Whether to grant the read-only roles on the project too; false when permissions/google grants them to the member already, applied with deny_data_reads=false, as its deny policy denies the member the state's objects."
  type        = bool
  default     = true
}

variable "deny_data_reads" {
  description = "Whether to attach a deny policy to the project that denies the member the data reads roles/viewer allows (log entries, BigQuery table data and more), all but Cloud Storage objects, which the state is. Managing deny policies takes the Deny Admin role (roles/iam.denyAdmin) on the organization: set false for a project outside an organization, or when the applier can't have that role."
  type        = bool
  default     = true
}

variable "state_bucket" {
  description = "The GCS bucket of the roots' state (the backend's bucket)."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$", var.state_bucket))
    error_message = "Use a GCS bucket name."
  }
}

variable "state_prefix" {
  description = "The prefix of the roots' state (the backend's prefix, see docs/configuration.md); empty for the whole bucket. With one, the member reads only the objects under it, through an IAM condition, which takes uniform bucket-level access on the bucket; it still lists every object's name in the bucket."
  type        = string
  default     = ""

  # The prefix goes into the condition's expression, as a string.
  validation {
    condition     = !can(regex("[\"\\\\]", var.state_prefix))
    error_message = "Use an object name prefix without \" or \\."
  }
}

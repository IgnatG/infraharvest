variable "project_id" {
  description = "Project infraharvest may read."
  type        = string
}

variable "member" {
  description = "Who runs infraharvest, e.g. serviceAccount:infraharvest@my-project.iam.gserviceaccount.com or user:me@example.com."
  type        = string

  validation {
    condition     = can(regex("^(user|serviceAccount|group|principal|principalSet):", var.member))
    error_message = "Use an IAM member such as serviceAccount:<email>."
  }
}

variable "deny_data_reads" {
  description = "Whether to attach a deny policy to the project that denies the member the data reads roles/viewer allows (log entries, BigQuery table data, objects and more). Managing deny policies takes the Deny Admin role (roles/iam.denyAdmin) on the organization: set false for a project outside an organization, or when the applier can't have that role."
  type        = bool
  default     = true
}

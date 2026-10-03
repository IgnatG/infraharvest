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

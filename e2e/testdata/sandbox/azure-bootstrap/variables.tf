variable "subscription_id" {
  description = "The sandbox subscription: the estate's resource group is created in it, and the read-only identity reads all of it. Best a subscription of its own, which holds nothing else."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F-]{36}$", var.subscription_id))
    error_message = "Use the subscription GUID."
  }
}

variable "location" {
  description = "Where the resource groups and the identities are; the estate is created in its resource group's location."
  type        = string
  default     = "uksouth"
}

variable "github_repository" {
  description = "The repository whose sandbox environment signs in as the identities, as owner/name, for the gh commands the outputs print."
  type        = string
  default     = "IgnatG/infraharvest"
}

variable "github_subject" {
  description = "The subject of the GitHub OIDC tokens the identities trust: the sandbox environment's jobs. The repository uses immutable subjects, with the owner's and the repository's IDs."
  type        = string
  default     = "repo:IgnatG@6107088/infraharvest@1420684118:environment:sandbox"
}

variable "identity_resource_group" {
  description = "The resource group of the two identities, apart from the estate's, which the fixture identity can change."
  type        = string
  default     = "infraharvest-ci"
}

variable "estate_resource_group" {
  description = "The resource group the sandbox test creates its estate in, and imports. It holds nothing between runs."
  type        = string
  default     = "infraharvest-sandbox"
}

variable "slot" {
  description = "Which sandbox subscription this is: \"\" for the first, which every estate uses, or \"second\", where the environments estate (TestSandboxAzureEnvironments) puts prod. It changes the names of the variables the gh commands set, AZURE_SECOND_ for the second, and the name of the custom role, which must be unique in the tenant."
  type        = string
  default     = ""

  validation {
    condition     = contains(["", "second"], var.slot)
    error_message = "Use \"\" for the first subscription or \"second\"."
  }
}

variable "first_readonly_principal_id" {
  description = "With slot = \"second\": the object ID of the first subscription's infraharvest-ci-readonly, its bootstrap's readonly_principal_id output. It gets Reader on this subscription too, as permissions/azure grants, for the environments estate's first import, which reads both subscriptions with one sign-in."
  type        = string
  default     = ""

  validation {
    condition     = var.first_readonly_principal_id == "" || can(regex("^[0-9a-fA-F-]{36}$", var.first_readonly_principal_id))
    error_message = "Use the principal's object ID, a GUID."
  }
}

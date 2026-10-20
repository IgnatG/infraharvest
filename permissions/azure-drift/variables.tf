variable "subscription_id" {
  description = "Subscription the provider authenticates against (it must contain or sit under the scope)."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F-]{36}$", var.subscription_id))
    error_message = "Use the subscription GUID."
  }
}

variable "scope" {
  description = "Where infraharvest may read: a subscription (/subscriptions/<id>) or a management group (/providers/Microsoft.Management/managementGroups/<name>). Defaults to the whole subscription."
  type        = string
  default     = null

  validation {
    condition     = var.scope == null || can(regex("^/(subscriptions/[0-9a-fA-F-]{36}|providers/Microsoft[.]Management/managementGroups/[^/]+)$", var.scope))
    error_message = "Use a subscription or management group ID."
  }
}

variable "principal_id" {
  description = "Object ID of the user, group, service principal or managed identity that runs infraharvest drift."
  type        = string
}

variable "assign_reader" {
  description = "Whether to assign Reader on the scope too; false when permissions/azure assigns it to the principal already."
  type        = bool
  default     = true
}

variable "state_storage_account_id" {
  description = "Resource ID of the storage account of the roots' state (the backend's storage_account_name), /subscriptions/<id>/resourceGroups/<group>/providers/Microsoft.Storage/storageAccounts/<name>."
  type        = string

  validation {
    condition     = can(regex("^/subscriptions/[0-9a-fA-F-]{36}/resourceGroups/[^/]+/providers/Microsoft[.]Storage/storageAccounts/[a-z0-9]{3,24}$", var.state_storage_account_id))
    error_message = "Use a storage account's resource ID."
  }
}

variable "state_container_name" {
  description = "The container of the roots' state (the backend's container_name)."
  type        = string
}

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
  description = "Object ID of the user, group, service principal or managed identity that runs infraharvest."
  type        = string
}

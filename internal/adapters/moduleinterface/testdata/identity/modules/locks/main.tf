# A local module, which calls itself: reading it must end.

variable "scope" {
  type = string
}

resource "azurerm_management_lock" "this" {
  lock_level = "CanNotDelete"
  name       = "lock"
  scope      = var.scope
}

module "again" {
  source = "../locks"

  scope = var.scope
}

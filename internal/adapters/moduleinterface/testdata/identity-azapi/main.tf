# identity's resources as azapi_resource, the credential renamed.

resource "azapi_resource" "this" {
  location  = var.location
  name      = var.name
  parent_id = var.parent_id
  type      = "Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31"
}

resource "azapi_resource" "federated_identity_credential" {
  for_each = var.federated_identity_credentials

  name      = each.key
  parent_id = azapi_resource.this.id
  type      = "Microsoft.ManagedIdentity/userAssignedIdentities/federatedIdentityCredentials@2023-01-31"
}

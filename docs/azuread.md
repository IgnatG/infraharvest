### Use with Azure Active Directory

Example:

```sh
export ARM_TENANT_ID=<TENANT_ID>
export ARM_CLIENT_ID=<CLIENT_ID>
export ARM_CLIENT_SECRET=<CLIENT_SECRET>
infraharvest import azuread --all --resources=user,application
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover azuread` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported AzureAD services:

*   `app_role_assignment`
    * `azuread_app_role_assignment`
*   `application`
    * `azuread_application`
*   `group`
    * `azuread_group`
*   `service_principal`
    * `azuread_service_principal`
*   `user`
    * `azuread_user`

# Azure

`infraharvest import azure` and `infraharvest discover azure` list the resources of one or more Azure subscriptions, or of one resource group, and generate configuration for the `hashicorp/azurerm` provider. The [services reference](reference/services.md#azure) lists the services `--services` takes and the Terraform types each one lists.

```sh
az login
export ARM_SUBSCRIPTION_ID=<subscription ID>
infraharvest discover azure --services='*'
```

## Signing in

infraharvest reads the `ARM_*` environment variables the azurerm provider documents, and picks the first way of signing in they set up, in the provider's order:

| Way | Variables |
|---|---|
| Client certificate | `ARM_CLIENT_CERTIFICATE_PATH` (a PFX or PEM file), with `ARM_CLIENT_CERTIFICATE_PASSWORD` if it has one, and `ARM_TENANT_ID`, `ARM_CLIENT_ID` |
| Client secret | `ARM_CLIENT_SECRET`, `ARM_TENANT_ID`, `ARM_CLIENT_ID` |
| OpenID Connect (workload identity federation) | `ARM_USE_OIDC=true`, `ARM_TENANT_ID`, `ARM_CLIENT_ID`, and the token request: `ARM_OIDC_REQUEST_URL` and `ARM_OIDC_REQUEST_TOKEN`, or in GitHub Actions the `ACTIONS_ID_TOKEN_REQUEST_*` variables its `id-token: write` permission sets |
| Managed identity | `ARM_USE_MSI=true`, with `ARM_CLIENT_ID` for a user-assigned identity, and `ARM_MSI_ENDPOINT` for an endpoint other than the usual one |
| Azure CLI | none: the account `az login` signed in, in the tenant `ARM_TENANT_ID` names, if set |

`ARM_USE_OIDC` and `ARM_USE_MSI` are booleans, read as the azurerm provider reads them: `true`, `True`, `TRUE`, `t`, `T` or `1` turns the method on; `false`, `False`, `FALSE`, `f`, `F`, `0`, empty or unset leaves it off; any other value, such as `yes`, stops the run with an error.

`ARM_AUXILIARY_TENANT_IDS` names up to three more tenants the credential may get tokens for, separated by `;`. `ARM_ENVIRONMENT` picks the cloud: `public` (the default), `usgovernment` or `china`, or `stack` for a cloud such as Azure Stack Hub whose endpoints `ARM_METADATA_HOSTNAME` serves.

Terraform's azurerm provider reads the same variables, so the generated roots plan with the same sign-in. Their provider block sets `subscription_id`.

## Permissions

The built-in **Reader** role on the subscriptions, or on a management group above them, is enough. The [`permissions/azure`](https://github.com/IgnatG/infraharvest/tree/main/permissions/azure) configuration assigns it; see [Permissions](permissions.md#azure).

## Subscriptions and resource groups

The import covers the subscription `ARM_SUBSCRIPTION_ID` names, unless one of these flags names others:

- `--subscriptions` takes subscription IDs.
- `--management-group` takes a management group ID, and adds every subscription under it, in its child groups too, that isn't disabled or deleted. Resource Graph finds them, with Reader (or `Microsoft.Management/managementGroups/read`) on the group.

Each subscription goes into roots of its own, so with several, a custom `--layout` must contain `{account}`. `--parallel` imports that many subscriptions at once, each with its own Terraform, and a subscription that fails is reported while the run goes on to the next.

`--resource-group` (`-g`) narrows the import to one resource group. Resources of a subscription span locations, so roots are split by resource group rather than by region: `<out>/azure/<subscription>/<resource group>/`, or `.../<subscription>/all/` for the whole subscription.

```sh
infraharvest import azure --management-group platform --services=resource_graph --all --parallel 2
```

## Resource Graph

The `resource_graph` service lists everything [Azure Resource Graph](https://learn.microsoft.com/azure/governance/resource-graph/overview) knows in the subscription, or in `--resource-group`, in one query, with the resource groups themselves, empty ones too. It reads from Resource Manager the child resources Resource Graph doesn't index, such as subnets and blob containers. Each resource gets its azurerm type from the catalogue's `inventory` entries, or else from [aztft](https://github.com/magodo/aztft), which asks Resource Manager when the resource ID alone can't tell, as between a function app and a web app. A resource azurerm has no type for, or that could be several, is logged and left out.

It is what `--services='*'` lists, together with `management_lock`, `security_center_contact` and `security_center_subscription_pricing`, which list what Resource Graph doesn't index: management locks are extension resources, set on a subscription, a resource group or a resource, and Microsoft Defender for Cloud's security contacts and plans belong to the subscription. With `--resource-group`, `management_lock` lists the locks on the group and its resources, and the two Defender services list nothing. Resource Graph needs the Reader role.

Some resources are left out by the default selection, with the reason: those another resource manages (`managedBy`), such as what is in an AKS cluster's `MC_*` node resource group, and what Azure creates itself, such as `NetworkWatcherRG`, the `DefaultResourceGroup-<region>` groups, Cloud Shell's storage groups, and the alert rules and action groups Application Insights adds.

## Services to name

`resource_group`, `virtual_network`, `subnet`, `network_security_group`, `public_ip`, `network_interface` and `managed_identity` list one kind of resource at a time through Resource Manager, without Resource Graph. What they list, `resource_graph` lists too, so `--services='*'` leaves them out, and naming them together with `resource_graph` would import the same resources twice. Use them where Resource Graph isn't available, such as Azure Stack Hub (`ARM_ENVIRONMENT=stack`) or an emulator, or to list only those kinds of resource:

```sh
infraharvest discover azure --services=resource_group,virtual_network,subnet,network_security_group
```

## Services that were removed

Earlier releases had a service for each kind of resource, such as `virtual_machine`, `keyvault`, `storage_account`, `cosmosdb` or `data_factory`. `resource_graph` lists all of those resources, with the types the current `azurerm` provider has: `azurerm_linux_virtual_machine` and `azurerm_windows_virtual_machine` rather than `azurerm_virtual_machine`, for example, and `azurerm_mssql_*` rather than `azurerm_sql_*`. A `--services` that names one of the old services fails with `unknown service`, and a selection file that names one needs `infraharvest discover azure` again. Storage blobs and Synapse managed private endpoints, which only their data-plane APIs list, are no longer imported.

## Tags

Tags come from Resource Graph, in one query for the subscription or resource group, for the selection file's rules, the picker and the report. Child resources Resource Graph doesn't index, such as subnets, have no tags of their own. azurerm has no provider-wide tags, so tags all resources of a root share become `local.tags`, which each resource sets, merged with its own; `hidden-` tags, which Azure and the portal set, stay on the resources.

## Filtering

`--id` takes the type without `azurerm_` and the full resource ID:

```sh
infraharvest import azure --services=virtual_network,subnet --all \
  --id=virtual_network=/subscriptions/<subscription>/resourceGroups/network/providers/Microsoft.Network/virtualNetworks/hub
```

No Azure lister takes `--tag` or `--where`; to choose by tag, use the selection file's rules. See [Filtering](usage.md#filtering).

## Modules

Azure Verified Modules declare the `azapi` provider. A root that calls one configures azapi for the same subscription and cloud as azurerm; see [Modules](modules.md).

# Azure

`infraharvest import azure` and `infraharvest discover azure` list the resources of one or more Azure subscriptions, or of one resource group, and generate configuration for the `hashicorp/azurerm` provider. The [services reference](reference/services.md#azure) lists the services `--services` takes and the Terraform types each one lists.

```sh
az login
export ARM_SUBSCRIPTION_ID=<subscription ID>
infraharvest discover azure --services='*'
```

## Signing in

infraharvest reads the `ARM_*` environment variables the azurerm provider documents, the way the provider reads them, and picks the first way of signing in they set up, in the provider's order, so that it lists as the identity the generated roots will plan as:

| Way | Variables |
|---|---|
| Client certificate | `ARM_CLIENT_CERTIFICATE` (a base64-encoded PFX) or `ARM_CLIENT_CERTIFICATE_PATH` (a PFX or PEM file), with `ARM_CLIENT_CERTIFICATE_PASSWORD` if it has one, and `ARM_TENANT_ID`, `ARM_CLIENT_ID` |
| Client secret | `ARM_CLIENT_SECRET` or `ARM_CLIENT_SECRET_FILE_PATH`, `ARM_TENANT_ID`, `ARM_CLIENT_ID` |
| OpenID Connect (workload identity federation) with a token | `ARM_USE_OIDC=true`, `ARM_TENANT_ID`, `ARM_CLIENT_ID`, and the ID token in `ARM_OIDC_TOKEN` or the file `ARM_OIDC_TOKEN_FILE_PATH` names, read again for each sign-in |
| AKS workload identity | `ARM_USE_AKS_WORKLOAD_IDENTITY=true`: OIDC with the token file, client ID and tenant ID AKS sets (`AZURE_FEDERATED_TOKEN_FILE`, `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`); an `ARM_CLIENT_ID` or `ARM_TENANT_ID` that differs stops the run |
| OpenID Connect in Azure Pipelines | `ARM_USE_OIDC=true`, `ARM_TENANT_ID`, `ARM_CLIENT_ID`, `ARM_ADO_PIPELINE_SERVICE_CONNECTION_ID` (or `ARM_OIDC_AZURE_SERVICE_CONNECTION_ID`), and the token request: `ARM_OIDC_REQUEST_URL` and `ARM_OIDC_REQUEST_TOKEN`, or the pipeline's `SYSTEM_OIDCREQUESTURI` and `SYSTEM_ACCESSTOKEN` |
| OpenID Connect with a token request | `ARM_USE_OIDC=true`, `ARM_TENANT_ID`, `ARM_CLIENT_ID`, and the token request: `ARM_OIDC_REQUEST_URL` and `ARM_OIDC_REQUEST_TOKEN`, or in GitHub Actions the `ACTIONS_ID_TOKEN_REQUEST_*` variables its `id-token: write` permission sets |
| Managed identity | `ARM_USE_MSI=true`, with `ARM_CLIENT_ID` for a user-assigned identity, and `ARM_MSI_ENDPOINT` (with `ARM_MSI_API_VERSION`, if it needs another) for an endpoint other than the usual one |
| Azure CLI | none: the account `az login` signed in, in the tenant `ARM_TENANT_ID` names, if set, unless `ARM_USE_CLI=false` |

`ARM_CLIENT_ID` can come from the file `ARM_CLIENT_ID_FILE_PATH` names; set both, they must agree, as must a token and a token file. infraharvest stops with a clear error rather than signing in some other way when `ARM_USE_OIDC` or `ARM_USE_AKS_WORKLOAD_IDENTITY` is set but there is no ID token or token request (and no `ARM_USE_MSI`), or when a client certificate, a secret or OIDC lacks `ARM_TENANT_ID` or `ARM_CLIENT_ID`: there the provider moves on to the next way, often the Azure CLI, and would plan as another identity. It also stops when `ARM_USE_CLI=false` leaves no way to sign in at all.

`ARM_USE_OIDC`, `ARM_USE_AKS_WORKLOAD_IDENTITY`, `ARM_USE_MSI` and `ARM_USE_CLI` are booleans, read as the azurerm provider reads them: `true`, `True`, `TRUE`, `t`, `T` or `1` turns the method on; `false`, `False`, `FALSE`, `f`, `F` or `0` turns it off, as does empty or unset, except for `ARM_USE_CLI`, which is on unless set to false; any other value, such as `yes`, stops the run with an error.

`ARM_AUXILIARY_TENANT_IDS` names up to three more tenants the credential may get tokens for, separated by `;`. `ARM_ENVIRONMENT` picks the cloud: `public` (the default), `usgovernment` or `china`, or `stack` for a cloud such as Azure Stack Hub whose endpoints `ARM_METADATA_HOSTNAME` serves.

Terraform's azurerm provider reads the same variables, so the generated roots plan with the same sign-in. They pin azurerm's newest release line (such as `~> 5.10`), and their provider block sets `subscription_id` and an empty `features` block, nothing else: azurerm 5 registers no resource providers on the subscription (`resource_provider_registrations` defaults to `"none"`), which the Reader role couldn't, so register any a later apply needs yourself, and it reads storage accounts through Resource Manager alone, without their keys, which Reader can't list: a storage account's queue and static website settings belong in the separate `azurerm_storage_account_*` resources (see [Permissions](permissions.md#azure)).

## Generated configuration

The configuration is written for azurerm 5, with its argument names, such as a Log Analytics workspace's `internet_ingestion_access_type` and `internet_query_access_type`, which replaced `internet_ingestion_enabled` and `internet_query_enabled`. Arguments at the values Azure and azurerm 5 give a resource that doesn't set them are left out where the plan stays the same, as the catalogue's `defaults` entries list them: a subnet's outbound access and network policies, a virtual network's private endpoint policies, a route table's BGP route propagation, a Key Vault's public network access and 90-day soft-delete retention, a Log Analytics workspace's PerGB2018 SKU, 30-day retention, unlimited daily quota, local authentication, resource-only permissions and internet access, a storage account's kind, access tier, TLS 1.2, HTTPS-only traffic, public network access, shared key and local user access, DNS endpoint type, encryption key types, file share retention and nested items' public access (`false` in azurerm 5), and a blob container's private access level. A Key Vault's network rules when it has none, and empty lists that would undo a separate resource, are left out whatever the plan says (`omit`): a Key Vault's `access_policy = []`, which would delete the policies `azurerm_key_vault_access_policy` resources manage, and a virtual network's `dns_servers = []`, which would fight `azurerm_virtual_network_dns_servers`.

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

The `resource_graph` service lists everything [Azure Resource Graph](https://learn.microsoft.com/azure/governance/resource-graph/overview) knows in the subscription, or in `--resource-group`, in one query, with the resource groups themselves, empty ones too. It reads from Resource Manager the child resources Resource Graph doesn't index, such as subnets and blob containers, and the data sources of Log Analytics workspaces, which Resource Manager lists only by kind: one list per workspace for each kind azurerm has a type for, Windows events and Windows performance counters. A child list, or a list of a workspace's data sources, that Resource Manager refuses (401, 403), throttles (429) or fails (5xx), or that gets no answer, fails the service, since resources may be missing, but what was listed is imported all the same; one for a child type a resource doesn't have is only logged. Each resource gets its azurerm type from the catalogue's `inventory` entries, or else from [aztft](https://github.com/magodo/aztft), which reads what the resource ID alone can't tell, as between a function app and a web app, from what the listing returned, and asks Resource Manager only for what it didn't return, a few resources at a time. A Log Analytics workspace's tables get their type from the table type they are listed with, without a request; tables of restored logs and of search job results, which azurerm has no type for, are left out. A resource azurerm has no type for, or that could be several, is logged and left out.

It is what `--services='*'` lists, together with `management_lock`, `security_center_contact` and `security_center_subscription_pricing`, which list what Resource Graph doesn't index: management locks are extension resources, set on a subscription, a resource group or a resource, and Microsoft Defender for Cloud's security contacts and plans belong to the subscription. With `--resource-group`, `management_lock` lists the locks on the group and its resources, and the two Defender services list nothing. `security_center_subscription_pricing` records each plan's tier, subplan and whether it is deprecated, and the default selection leaves out, with the reason, the plans left on the Free tier, which every subscription has: select one to import it. Resource Graph needs the Reader role.

Some resources are left out by the default selection, with the reason: those another resource manages (`managedBy`), such as what is in an AKS cluster's `MC_*` node resource group, or a virtual machine's OS disk, which the machine's `os_disk` block manages (a data disk attached to a machine is imported, with the machine's `azurerm_virtual_machine_data_disk_attachment`), or the network interface of a private endpoint or a Private Link service, which the endpoint or service creates and deletes with itself, and what Azure creates itself, such as `NetworkWatcherRG` and its network watchers (the flow logs and connection monitors in it are imported), the `DefaultResourceGroup-<region>` groups, Cloud Shell's storage groups, Azure Backup's `AzureBackupRG_<region>_<n>` groups with their restore point collections, the `AzureBackupProtectionLock` delete lock Azure Backup adds to a storage account whose file shares it protects, a management lock in a group the selection leaves out, what AKS adds when Container insights or managed Prometheus is enabled (the `MSCI-*` and `MSProm-*` data collection rules and endpoints and the `*RecordingRulesRuleGroup*` Prometheus rule groups), the `SecurityCenterFree(...)` and `ContainerInsights(...)` Log Analytics solutions, the virtual machine extensions Microsoft Defender for Cloud and Azure Policy install (`MDE.Linux`, `MDE.Windows`, `AzureSecurityLinuxAgent`, `AzureSecurityWindowsAgent`, `AzurePolicyforLinux`, `AzurePolicyforWindows`), the alert rules and action groups Application Insights adds, and the hundreds of tables and saved searches built into every Log Analytics workspace: a built-in table is imported only once its plan or retention differs from the default, and a saved search only if a solution (such as LogManagement) didn't add it. What is in a resource group left out whole, such as an `MC_*` or a `DefaultResourceGroup-<region>` group, is listed so that it can be selected, but without the child resources and workspace data sources that would take a request each to list; `--resource-group` naming the group lists them too.

The types are those the current `azurerm` provider has, such as `azurerm_linux_virtual_machine` and `azurerm_windows_virtual_machine` rather than `azurerm_virtual_machine`, and `azurerm_mssql_*` rather than `azurerm_sql_*`. Storage blobs and Synapse managed private endpoints, which only their data-plane APIs list, aren't imported.

Routes, subnets and network security rules are imported as resources of their own (`azurerm_route`, `azurerm_subnet`, `azurerm_network_security_rule`), as azurerm recommends, so route tables, virtual networks and network security groups are generated without their inline `route`, `subnet` and `security_rule`: inline, both would manage the same settings and undo each other on every apply.

## Services to name

`resource_group`, `virtual_network`, `subnet`, `network_security_group`, `public_ip`, `network_interface` and `managed_identity` list one kind of resource at a time through Resource Manager, without Resource Graph. What they list, `resource_graph` lists too, so `--services='*'` leaves them out, and naming them together with `resource_graph` would import the same resources twice. Use them where Resource Graph isn't available, such as Azure Stack Hub (`ARM_ENVIRONMENT=stack`) or an emulator, or to list only those kinds of resource:

```sh
infraharvest discover azure --services=resource_group,virtual_network,subnet,network_security_group
```

`subnet` and `network_security_group` take the subnets and rules from the list of networks and of security groups, which carries them, so they make no request per network or group; only for one that comes back without them or with none, as some emulators answer, are they listed on their own.

## Tags

Tags come with the listing, for the selection file's rules, the picker and the report: `resource_graph` reads them from the rows Resource Graph returns, and the Resource Manager services from their list responses, so reading them takes no request of its own. Child resources such as subnets have no tags of their own. azurerm has no provider-wide tags, so tags all resources of a root share become `local.tags`, which each resource sets, merged with its own; `hidden-` tags, which Azure and the portal set, stay on the resources.

## Filtering

`--id` takes the type without `azurerm_` and the full resource ID:

```sh
infraharvest import azure --services=virtual_network,subnet --all \
  --id=virtual_network=/subscriptions/<subscription>/resourceGroups/network/providers/Microsoft.Network/virtualNetworks/hub
```

No Azure lister takes `--tag` or `--where`; to choose by tag, use the selection file's rules. See [Filtering](usage.md#filtering).

## Modules

A curated module is called only when it accepts azurerm's newest major release: the Azure Verified Modules managed identity module, 0.5.3, accepts azurerm below 5 only, so user-assigned identities and their federated credentials stay in the root as plain resources, and the report says why. Azure Verified Modules declare the `azapi` provider: a root that calls one configures azapi for the same subscription and cloud as azurerm, and one that calls none has no azapi, modtm or random provider. See [Modules](modules.md).

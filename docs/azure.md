# Use with Azure

## Authentication

### Supported Methods

- [Azure CLI](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/azure_cli)
- [managed identities for Azure resources](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/managed_service_identity)
- [Service Principal with Client Certificate](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/service_principal_client_certificate)
- [Service Principal with Client Secret](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/service_principal_client_secret)
- [Service Principal with Open ID Connect](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/service_principal_oidc)

The provider signs in with the first method whose settings are present, in this order: client certificate (`ARM_CLIENT_CERTIFICATE_PATH`), client secret (`ARM_CLIENT_SECRET`), OpenID Connect (`ARM_USE_OIDC` plus an ID token request URL and token), managed identity (`ARM_USE_MSI`), and otherwise the Azure CLI login. `ARM_SUBSCRIPTION_ID` is always required.

Other settings:

- `ARM_ENVIRONMENT`: the Azure cloud, `public` (default), `usgovernment`, `china`, or `stack` for a custom cloud such as Azure Stack Hub, whose endpoints `ARM_METADATA_HOSTNAME` serves (as for the azurerm provider: a host name, read over HTTPS).
- `ARM_AUXILIARY_TENANT_IDS`: up to 3 more tenant IDs, separated by `;`, for resources linked across tenants.

### Examples

``` sh
# Using Azure CLI (az login)
export ARM_SUBSCRIPTION_ID=[SUBSCRIPTION_ID]
export ARM_TENANT_ID=[TENANT_ID] # optional, to use a tenant other than the CLI's default

# Using Managed identities for Azure resources
export ARM_SUBSCRIPTION_ID=[SUBSCRIPTION_ID]
export ARM_CLIENT_ID=[CLIENT_ID]  # only necessary for user assigned identity
export ARM_USE_MSI=true
export ARM_MSI_ENDPOINT=[ARM_MSI_ENDPOINT] # only necessary when the msi endpoint is different than the well-known one

# Using Service Principal with Client Certificate
export ARM_SUBSCRIPTION_ID=[SUBSCRIPTION_ID]
export ARM_CLIENT_ID=[CLIENT_ID]
export ARM_TENANT_ID=[TENANT_ID]
export ARM_CLIENT_CERTIFICATE_PATH="/path/to/my/client/certificate.pfx"
export ARM_CLIENT_CERTIFICATE_PASSWORD=[CLIENT_CERTIFICATE_PASSWORD]

# Using Service Principal with Client Secret
export ARM_SUBSCRIPTION_ID=[SUBSCRIPTION_ID]
export ARM_CLIENT_ID=[CLIENT_ID]
export ARM_TENANT_ID=[TENANT_ID]
export ARM_CLIENT_SECRET=[CLIENT_SECRET]

# Using Service Principal with Open ID Connect (GitHub Actions)
export ARM_SUBSCRIPTION_ID=[SUBSCRIPTION_ID]
export ARM_CLIENT_ID=[CLIENT_ID]
export ARM_TENANT_ID=[TENANT_ID]
export ARM_USE_OIDC=true
# The ID token is requested from ARM_OIDC_REQUEST_URL with ARM_OIDC_REQUEST_TOKEN;
# in a GitHub Actions job with `permissions: id-token: write` they default to
# ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN.

infraharvest import azure --all --resources=resource_group
infraharvest import azure --all --resource-group=my_resource_group --resources=virtual_network,resource_group
infraharvest import azure --all --resources=resource_group --filter=resource_group=/subscriptions/<Subscription id>/resourceGroups/<RGNAME>
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover azure` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

## List of supported Azure resources

*   `analysis`
    * `azurerm_analysis_services_server`
*   `app_service`
    * `azurerm_app_service`
*   `application_gateway`
    * `azurerm_application_gateway`
*   `container`
    * `azurerm_container_group`
    * `azurerm_container_registry`
    * `azurerm_container_registry_webhook`
*   `cosmosdb`
    * `azurerm_cosmosdb_account`
    * `azurerm_cosmosdb_sql_container`
    * `azurerm_cosmosdb_sql_database`
    * `azurerm_cosmosdb_table`
*   `data_factory`
    * `azurerm_data_factory`
    * `azurerm_data_factory_custom_dataset`
    * `azurerm_data_factory_data_flow`
    * `azurerm_data_factory_dataset_azure_blob`
    * `azurerm_data_factory_dataset_binary`
    * `azurerm_data_factory_dataset_cosmosdb_sqlapi`
    * `azurerm_data_factory_dataset_delimited_text`
    * `azurerm_data_factory_dataset_http`
    * `azurerm_data_factory_dataset_json`
    * `azurerm_data_factory_dataset_mysql`
    * `azurerm_data_factory_dataset_parquet`
    * `azurerm_data_factory_dataset_postgresql`
    * `azurerm_data_factory_dataset_snowflake`
    * `azurerm_data_factory_dataset_sql_server_table`
    * `azurerm_data_factory_integration_runtime_azure`
    * `azurerm_data_factory_integration_runtime_azure_ssis`
    * `azurerm_data_factory_integration_runtime_managed`
    * `azurerm_data_factory_integration_runtime_self_hosted`
    * `azurerm_data_factory_linked_custom_service`
    * `azurerm_data_factory_linked_service_azure_blob_storage`
    * `azurerm_data_factory_linked_service_azure_databricks`
    * `azurerm_data_factory_linked_service_azure_file_storage`
    * `azurerm_data_factory_linked_service_azure_function`
    * `azurerm_data_factory_linked_service_azure_search`
    * `azurerm_data_factory_linked_service_azure_sql_database`
    * `azurerm_data_factory_linked_service_azure_table_storage`
    * `azurerm_data_factory_linked_service_cosmosdb`
    * `azurerm_data_factory_linked_service_data_lake_storage_gen2`
    * `azurerm_data_factory_linked_service_key_vault`
    * `azurerm_data_factory_linked_service_kusto`
    * `azurerm_data_factory_linked_service_mysql`
    * `azurerm_data_factory_linked_service_odata`
    * `azurerm_data_factory_linked_service_postgresql`
    * `azurerm_data_factory_linked_service_sftp`
    * `azurerm_data_factory_linked_service_snowflake`
    * `azurerm_data_factory_linked_service_sql_server`
    * `azurerm_data_factory_linked_service_synapse`
    * `azurerm_data_factory_linked_service_web`
    * `azurerm_data_factory_pipeline`
    * `azurerm_data_factory_trigger_blob_event`
    * `azurerm_data_factory_trigger_schedule`
    * `azurerm_data_factory_trigger_tumbling_window`
*   `database`
    * `azurerm_mariadb_configuration`
    * `azurerm_mariadb_database`
    * `azurerm_mariadb_firewall_rule`
    * `azurerm_mariadb_server`
    * `azurerm_mariadb_virtual_network_rule`
    * `azurerm_mysql_configuration`
    * `azurerm_mysql_database`
    * `azurerm_mysql_firewall_rule`
    * `azurerm_mysql_server`
    * `azurerm_mysql_virtual_network_rule`
    * `azurerm_postgresql_configuration`
    * `azurerm_postgresql_database`
    * `azurerm_postgresql_firewall_rule`
    * `azurerm_postgresql_server`
    * `azurerm_postgresql_virtual_network_rule`
    * `azurerm_sql_active_directory_administrator`
    * `azurerm_sql_database`
    * `azurerm_sql_elasticpool`
    * `azurerm_sql_failover_group`
    * `azurerm_sql_firewall_rule`
    * `azurerm_sql_server`
    * `azurerm_sql_virtual_network_rule`
*   `databricks`
    * `azurerm_databricks_workspace`
*   `disk`
    * `azurerm_managed_disk`
*   `dns`
    * `azurerm_dns_a_record`
    * `azurerm_dns_aaaa_record`
    * `azurerm_dns_caa_record`
    * `azurerm_dns_cname_record`
    * `azurerm_dns_mx_record`
    * `azurerm_dns_ns_record`
    * `azurerm_dns_ptr_record`
    * `azurerm_dns_srv_record`
    * `azurerm_dns_txt_record`
    * `azurerm_dns_zone`
*   `eventhub`
    * `azurerm_eventhub`
    * `azurerm_eventhub_consumer_group`
    * `azurerm_eventhub_namespace`
    * `azurerm_eventhub_namespace_authorization_rule`
*   `load_balancer`
    * `azurerm_lb`
    * `azurerm_lb_backend_address_pool`
    * `azurerm_lb_nat_rule`
    * `azurerm_lb_probe`
*   `network_interface`
    * `azurerm_network_interface`
*   `network_security_group`
    * `azurerm_network_security_group`
    * `azurerm_network_security_rule`
*   `network_watcher`
    * `azurerm_network_packet_capture`
    * `azurerm_network_watcher`
    * `azurerm_network_watcher_flow_log`
*   `private_dns`
    * `azurerm_private_dns_a_record`
    * `azurerm_private_dns_aaaa_record`
    * `azurerm_private_dns_cname_record`
    * `azurerm_private_dns_mx_record`
    * `azurerm_private_dns_ptr_record`
    * `azurerm_private_dns_srv_record`
    * `azurerm_private_dns_txt_record`
    * `azurerm_private_dns_zone`
    * `azurerm_private_dns_zone_virtual_network_link`
*   `private_endpoint`
    * `azurerm_private_endpoint`
    * `azurerm_private_link_service`
*   `public_ip`
    * `azurerm_public_ip`
    * `azurerm_public_ip_prefix`
*   `purview`
    * `azurerm_purview_account`
*   `redis`
    * `azurerm_redis_cache`
*   `resource_group`
    * `azurerm_management_lock`
    * `azurerm_resource_group`
*   `route_table`
    * `azurerm_route`
    * `azurerm_route_filter`
    * `azurerm_route_table`
*   `scaleset`
    * `azurerm_virtual_machine_scale_set`
*   `security_center`
    * `azurerm_security_center_contact`
    * `azurerm_security_center_subscription_pricing`
*   `storage_account`
    * `azurerm_storage_account`
    * `azurerm_storage_blob`
    * `azurerm_storage_container`
*   `subnet`
    * `azurerm_subnet`
    * `azurerm_subnet_nat_gateway_association`
    * `azurerm_subnet_network_security_group_association`
    * `azurerm_subnet_route_table_association`
    * `azurerm_subnet_service_endpoint_storage_policy`
*   `synapse`
    * `azurerm_synapse_firewall_rule`
    * `azurerm_synapse_managed_private_endpoint`
    * `azurerm_synapse_private_link_hub`
    * `azurerm_synapse_spark_pool`
    * `azurerm_synapse_sql_pool`
    * `azurerm_synapse_workspace`
*   `virtual_machine`
    * `azurerm_ssh_public_key`
    * `azurerm_virtual_machine`
*   `virtual_network`
    * `azurerm_virtual_network`

## Notes

### Virtual networks and subnets

The `virtual_network` service lists virtual networks only. Subnets are listed by the `subnet` service, as `azurerm_subnet` resources, so import both to bring a network and its subnets under Terraform.

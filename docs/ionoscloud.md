# Use with IONOS Cloud

## Authentication

Proper credential must be configured, before it can be used.

You can set the environment variables for *HTTP basic authentication*:

```sh
export IONOS_USERNAME="username"
export IONOS_PASSWORD="password"
```

Or you can use *token authentication*:

```sh
export IONOS_TOKEN="token"
```

## Example

```sh
infraharvest import ionoscloud --all --resources=datacenter,server
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover ionoscloud` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

## List of supported IONOS Cloud resources

* [`application_loadbalancer`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/application_loadbalancer)
* [`application_loadbalancer_forwardingrule`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/application_loadbalancer_forwardingrule)
* [`backup_unit`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/backup_unit)
* [`certificate`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/certificate)
* [`container_registry`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/container_registry)
* [`container_registry_token`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/container_registry_token)
* [`datacenter`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/datacenter)
* [`dataplatform_cluster`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dataplatform_cluster)
* [`dataplatform_node_pool`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dataplatform_node_pool)
* [`dns_record`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dns_record)
* [`dns_zone`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dns_zone)
* [`firewall`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/firewall)
* [`group`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/group)
* [`ipblock`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/ipblock)
* [`ipfailover`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/ipfailover)
* [`k8s_cluster`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/k8s_cluster)
* [`k8s_node_pool`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/k8s_node_pool)
* [`lan`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/lan)
* [`loadbalancer`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/loadbalancer)
* [`logging_pipeline`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/logging_pipeline)
* [`mongo_cluster`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dbaas_mongo_cluster)
* [`mongo_user`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dbaas_mongo_user)
* [`natgateway`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/natgateway)
* [`natgateway_rule`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/natgateway_rule)
* [`networkloadbalancer`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/networkloadbalancer)
* [`networkloadbalancer_forwardingrule`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/networkloadbalancer_forwardingrule)
* [`nic`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/nic)
* [`pg_cluster`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dbaas_pgsql_cluster)
* [`pg_database`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dbaas_pgsql_database)
* [`pg_user`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/dbaas_pgsql_user)
* [`private_crossconnect`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/private_crossconnect)
* [`s3_key`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/s3_key)
* [`server`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/server)
* [`share`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/share)
* [`target_group`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/target_group)
* [`user`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/user)
* [`volume`](https://registry.terraform.io/providers/ionos-cloud/ionoscloud/latest/docs/resources/volume)

Resources that Terraform can't import go into `rejected.hcl` under Terraform's errors, and the report lists them (see [Output](../README.md#output)).

#### Notes:
 - A server must have a `NIC` and a `volume` attached to be imported.
 - A server must also have a `BootVolume` set.

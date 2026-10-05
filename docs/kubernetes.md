### Use with Kubernetes

Example:

```sh
infraharvest import kubernetes --all --resources=deployments,services,storageclasses
infraharvest import kubernetes --all --resources=deployments,services,storageclasses --filter=deployment=name1:name2:name3
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover kubernetes` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

All Kubernetes resources that are currently supported by the Kubernetes provider, are also supported by this module. Here is the list of resources which are currently supported by Kubernetes provider v.1.4:

*   `clusterrolebinding`
    * `kubernetes_cluster_role_binding`
*   `configmaps`
    * `kubernetes_config_map`
*   `deployments`
    * `kubernetes_deployment`
*   `horizontalpodautoscalers`
    * `kubernetes_horizontal_pod_autoscaler`
*   `limitranges`
    * `kubernetes_limit_range`
*   `namespaces`
    * `kubernetes_namespace`
*   `persistentvolumes`
    * `kubernetes_persistent_volume`
*   `persistentvolumeclaims`
    * `kubernetes_persistent_volume_claim`
*   `pods`
    * `kubernetes_pod`
*   `replicationcontrollers`
    * `kubernetes_replication_controller`
*   `resourcequotas`
    * `kubernetes_resource_quota`
*   `secrets`
    * `kubernetes_secret`
*   `services`
    * `kubernetes_service`
*   `serviceaccounts`
    * `kubernetes_service_account`
*   `statefulsets`
    * `kubernetes_stateful_set`
*   `storageclasses`
    * `kubernetes_storage_class`
    
#### Known issues

* The Terraform Kubernetes provider rejects resources with ":" characters in their names (as they don't meet DNS-1123), while Kubernetes allows them for certain types, e.g. ClusterRoleBinding.

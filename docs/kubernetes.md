### Use with Kubernetes

Example:

```sh
infraharvest import kubernetes --all --resources=deployments,services,storageclasses
infraharvest import kubernetes --all --resources=deployments,services,storageclasses --filter=deployment=name1:name2:name3
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover kubernetes` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

infraharvest connects with your kubeconfig, as kubectl does. Credential plugins in the kubeconfig (`exec`) work; the old built-in `gcp` auth provider doesn't. For GKE, install `gke-gcloud-auth-plugin` and run `gcloud container clusters get-credentials` again, which writes the plugin into the kubeconfig.

#### Supported resources

infraharvest asks the cluster which API resources it serves (discovery, using the version the cluster prefers for each group) and imports every one that supports `list` and has a resource type in the Terraform kubernetes provider v3. The type is `kubernetes_` plus the kind in snake case: a `Deployment` becomes `kubernetes_deployment`, an `APIService` `kubernetes_api_service`, and where the provider only has a `_v1` type, that one: a `DaemonSet` becomes `kubernetes_daemon_set_v1`. Kinds without a provider type, such as events and custom resources, are left out. If some API groups fail discovery (an aggregated API whose service is down, for example), infraharvest logs them and lists the rest.

Each resource is a service named by its plural resource name, as `kubectl api-resources` shows it: `deployments`, `services`, `configmaps`, `clusterrolebindings`, and so on. `infraharvest import kubernetes list` prints the services your cluster offers, and `infraharvest discover kubernetes` lists the objects themselves into a selection file.

Objects owned by another object (with `ownerReferences`, such as the pods of a ReplicaSet or the ReplicaSets of a Deployment) are skipped, since their owner creates them. Namespaced objects are listed across all namespaces, imported with the ID `<namespace>/<name>`; cluster-scoped objects, such as namespaces or cluster roles, with `<name>`.

The provider types infraharvest knows are listed in [providers/kubernetes/supported_types.go](../providers/kubernetes/supported_types.go), taken from the docs of hashicorp/kubernetes v3.3.0.

#### Known issues

* The Terraform Kubernetes provider rejects resources with ":" characters in their names (as they don't meet DNS-1123), while Kubernetes allows them for certain types, e.g. ClusterRoleBinding.

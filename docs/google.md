# Google Cloud

`infraharvest import google` and `infraharvest discover google` list the resources of one or more Google Cloud projects, region by region, and generate configuration for the `hashicorp/google` provider. Most of a project is listed through [Cloud Asset Inventory](#cloud-asset-inventory); the [services reference](reference/services.md#google-cloud) lists the services `--services` takes and the Terraform types each one lists.

```sh
gcloud auth application-default login
infraharvest discover google --services='*' --projects=my-project --regions=global,europe-west2
```

## Credentials

infraharvest uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials), as the Google Cloud client libraries and the Terraform provider do: the file `GOOGLE_APPLICATION_CREDENTIALS` names (a service account key, or a workload identity federation configuration), else the credentials `gcloud auth application-default login` saved, else, on Google Cloud, those of the environment, such as Cloud Shell's or a Compute Engine instance's service account. They are used with the `cloud-platform` scope.

Terraform's google provider finds the same credentials, so the generated roots plan with them. Their provider block sets the project, the region, and `add_terraform_attribution_label = false`: otherwise the provider would add its `goog-terraform-provisioned` label to every imported resource, and each would plan a change to its labels.

`--rate-limit` caps the requests a second to each Google API, as quotas are per API and project.

## Permissions

`roles/viewer` reads resource configuration, `roles/iam.securityReviewer` the IAM policies on resources, and `roles/cloudasset.viewer` searches the project through Cloud Asset Inventory. The search also needs the Cloud Asset API (`cloudasset.googleapis.com`) enabled in the project your credentials' quota goes to. The [`permissions/google`](https://github.com/IgnatG/infraharvest/tree/main/permissions/google) configuration grants the three roles on a project; see [Permissions](permissions.md#google-cloud).

## Projects

Every import names its projects; there is no default:

- `--projects` takes project IDs.
- `--organization` takes an organization ID and adds every active project in it, in its folders and their subfolders too.
- `--folders` takes folder IDs and adds every active project in them and their subfolders.

Listing an organization's or a folder's projects needs `resourcemanager.projects.list` and `resourcemanager.folders.list`, which `roles/browser` and `roles/viewer` grant. Each project goes into roots of its own, so with several, a custom `--layout` must contain `{account}` (or `{provider}/{service}`, which gets the project and region put in). `--parallel` imports that many projects at once, each with its own Terraform, and a project that fails is reported while the run goes on to the next.

```sh
infraharvest import google --organization 123456789012 --services='*' --regions=global,europe-west2 --all --parallel 4
```

## Regions

`--regions` lists the regions to import, each in turn, into roots of its own: `<out>/google/<project>/<region>/`. The default, `global`, holds the resources that belong to no region, such as networks, firewall rules, global addresses and service accounts, and those in a multi-region, such as a bucket in `US`. A region's import covers the region and its zones, such as the region's subnetworks and the instances in its zones. Each resource goes into one root only, so import `global` with your regions: `--regions=global,europe-west2`. Each region is looked up through the Compute Engine API first, and one that doesn't exist fails the project's import.

## Services

`--services='*'` lists these services:

| Service | What it lists | Imports |
|---|---|---|
| `cloud_asset` | every type the catalogue maps from [Cloud Asset Inventory](#cloud-asset-inventory): Compute Engine, GKE, Cloud Storage, Pub/Sub, Cloud SQL instances, Memorystore, BigQuery, Cloud Functions, Cloud Run, Cloud Tasks, Cloud Build triggers, Secret Manager, Artifact Registry, KMS, DNS zones, service accounts, custom roles, log-based metrics, Cloud Monitoring alert policies, notification channels and uptime checks, and the project itself | global and regions |
| `iam` | the project's IAM policy, one `google_project_iam_member` per member of each role, conditional grants with their condition's title | global |
| `dns` | the record sets of the project's Cloud DNS zones | global |
| `cloudsql` | the databases of the Cloud SQL instances, except read replicas' | the instances' regions |
| `schedulerJobs` | Cloud Scheduler jobs | regions |

The last four list, each through its own API, what Cloud Asset Inventory doesn't find. A project without the Cloud DNS, Cloud SQL Admin or Cloud Scheduler API enabled has nothing for that service to list, which is logged rather than reported as a failure.

`iam` lists only the IAM members, `dns` only the record sets and `cloudsql` only the databases. Service accounts, custom roles, DNS zones and Cloud SQL instances come from `cloud_asset`, so add `cloud_asset`, or use `*`, for those.

The default selection leaves out the grants Google Cloud makes itself: a service agent role (`roles/pubsub.serviceAgent`, `roles/compute.instanceGroupManagerServiceAgent`, ...), which it grants a service's agent when the service is enabled, and `roles/editor` for the Google APIs service agent (`<number>@cloudservices.gserviceaccount.com`) and the Compute Engine and App Engine default service accounts, `roles/cloudbuild.builds.builder` for the legacy Cloud Build service account (`<number>@cloudbuild.gserviceaccount.com`), and the `roles/iam.serviceAccountTokenCreator` older projects gave the Pub/Sub service agent. It also leaves out grants to deleted principals (`deleted:...`). The selection file can include them.

### Types that aren't listed

`cloud_asset` lists only what Cloud Asset Inventory finds and the catalogue can map, so these types aren't imported:

| Types | Why |
|---|---|
| `google_storage_bucket_iam_policy`, `google_storage_bucket_iam_binding`, `google_storage_bucket_iam_member`, `google_storage_notification`, and the bucket and default object ACLs | Cloud Asset Inventory's resource search returns no IAM policies or notifications |
| `google_monitoring_group` | Cloud Asset Inventory has no asset type for monitoring groups |
| `google_compute_managed_ssl_certificate`, `google_compute_region_ssl_certificate` | Google-managed and self-managed certificates share one asset type and name pattern, so the catalogue can't tell them apart |

## Cloud Asset Inventory

The `cloud_asset` service lists a project through [Cloud Asset Inventory](https://cloud.google.com/asset-inventory/docs/overview) instead of each service's API: a search for the asset types the catalogue's `inventory` entries map to Terraform types (see [Inventory types](catalogue.md#inventory-types)). The project is searched once per run: a region's import takes the resources in the region and its zones, and the import of `global` takes the rest: global resources, and those in a multi-region such as a bucket in `US`. A resource the catalogue maps to no type, or to several, is logged and left out, as is one being deleted.

The search needs `cloudasset.assets.searchAllResources` on the project, which `roles/cloudasset.viewer` grants, and the Cloud Asset API enabled in the project your credentials' quota goes to. Search results can lag behind changes by a few minutes, and by hours for Cloud Functions, so something just created may not be listed yet.

Each resource's ID is its full name without the API's host, such as `projects/my-project/global/networks/shared-vpc`, with the project's ID where Google's name has its number, and the catalogue builds the ID Terraform imports it with. Its labels, location, display name and description are recorded too, so the catalogue's rules leave out by default what Google creates on its own: the default network and its firewall rules, the routes Google Cloud creates with networks and subnetworks, what GKE creates for its clusters (auto-provisioned node pools, ingress instance groups and the load balancers of LoadBalancer Services, which it describes with the Service's name, among it), the clusters Cloud Composer creates and their node pools, the default service accounts, the Cloud Run service each 2nd gen function runs on, the buckets and repositories Cloud Functions and Cloud Run create, the buckets Cloud Build, Container Registry, App Engine and Cloud Composer create, and the Pub/Sub topics and subscriptions Eventarc, Cloud Functions, Artifact Analysis and Cloud Build create.

An instance manages its boot disk in its `boot_disk` block, so a disk that is the boot disk of an instance the search finds is left out by default (selectable): imported as `google_compute_disk` or `google_compute_region_disk` too, it would have two owners. To tell which disks those are, a project whose search finds both instances and disks has its instances listed once per run, every zone in one aggregated list that returns no more than each instance's name and its disks' sources and boot flags; that needs `compute.instances.list`, which `roles/viewer` grants. A data disk is imported as a disk, and the instance's `attached_disk` block attaches it by its `source`. If the instances can't be listed, or a zone can't be reached, that is logged, and the project's disks are left out by default too, as each may be a boot disk; select them to import them.

GKE manages the node pools of an Autopilot cluster, its default pool among them, creating and deleting them itself, so those are left out by default (selectable) too. The search doesn't say whether a cluster is an Autopilot cluster, so a project whose search finds node pools has its clusters listed once per run, every location in one list that returns no more than each cluster's name, location and whether it is an Autopilot cluster; that needs `container.clusters.list`, which `roles/viewer` grants. The cluster itself is imported, with `enable_autopilot`, and without its `node_pool` blocks, as every cluster is. If the clusters can't be listed, or a zone's can't, that is logged, and the project's node pools are left out by default, as GKE may manage each; select them to import them.

## Labels

Labels are recorded, where the listers read them (every resource `cloud_asset` lists), for the selection file's rules, the picker and the report.

Since the google provider's version 5.0, a resource's `labels` holds only the labels of its configuration, which an import leaves empty, and `effective_labels` every label the resource has. So Terraform generates no labels, and infraharvest writes each resource's `labels` from its `effective_labels`, without the `goog-` labels, which Google and the provider set themselves (such as `goog-terraform-provisioned`): those stay on the resources, outside the configuration. The plan then updates `labels` and `terraform_labels` in place, in state alone: `effective_labels`, and the resource, stay as they are, and `terraform apply` changes nothing in the cloud. The verification gate accepts those updates, and the README of each root says to expect them.

The labels all resources of a root share move to the provider's `default_labels`, as `local.labels`, if the plan stays the same: not when another resource that takes labels has none, as `default_labels` would label it.

## The google-beta provider

`--beta` generates configuration for the `google-beta` provider instead, for resources and arguments only it has. Some curated modules create resources through `google-beta` either way, such as the network module's network; a root that calls one configures `google-beta` like its own provider (see [Modules](modules.md)).

## Filtering

`--id` takes the type without `google_` and the ID the lister records: for what `cloud_asset` lists, the full name, or a bucket's name:

```sh
infraharvest import google --services=cloud_asset --projects=my-project --all --id=storage_bucket=my-logs
infraharvest import google --services=cloud_asset --projects=my-project --regions=global,europe-west2 --all \
  --id=compute_network=projects/my-project/global/networks/shared-vpc
```

No Google Cloud lister takes `--tag` or `--where`; to choose by label, use the selection file's rules. See [Filtering](usage.md#filtering).

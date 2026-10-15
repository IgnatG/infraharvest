# Modules

## Curated modules

Clusters of resources that a curated public module can manage move into a call of that module, at an exact version. For each cluster, infraharvest maps the generated configuration onto the module's inputs. It declines a cluster when the module can't express one of its settings, rather than drop the setting. It then plans, and takes back any call whose resources would plan differently than they did in the root, or for which the module would create anything else. The root README and the report list each call, and each cluster that stayed in the root with the reason. These modules are mapped so far:

| Resources | Module |
|---|---|
| `aws_s3_bucket` with its versioning, encryption, public access block, ownership controls, lifecycle and policy | [`terraform-aws-modules/s3-bucket/aws`](https://registry.terraform.io/modules/terraform-aws-modules/s3-bucket/aws) 5.17.0 |
| `aws_iam_role` with its policy attachments, and an inline policy and instance profile named after it | [`terraform-aws-modules/iam/aws//modules/iam-role`](https://registry.terraform.io/modules/terraform-aws-modules/iam/aws/latest/submodules/iam-role) 6.8.2 |
| `aws_vpc` with its subnets, internet gateway, route tables and their associations, and its NAT gateways with their Elastic IPs, built as the module builds them: public subnets sharing a route table to the internet gateway, and private ones either sharing a route table to one NAT gateway (`single_nat_gateway`) or, with a NAT gateway in each zone's public subnet, each with a route table of its own to the gateway in its zone (`one_nat_gateway_per_az`), every resource named after the VPC. The default routes the root keeps in its route tables are imported as the module's `aws_route` resources | [`terraform-aws-modules/vpc/aws`](https://registry.terraform.io/modules/terraform-aws-modules/vpc/aws) 6.7.3 |
| `google_compute_network` (custom mode) with its subnetworks, their secondary ranges and flow logs, and its firewall rules; the network is created through `google-beta` | [`terraform-google-modules/network/google`](https://registry.terraform.io/modules/terraform-google-modules/network/google) 18.3.1 |
| `google_storage_bucket` with its versioning, lifecycle rules, soft delete, retention, encryption, CORS, website and logging settings | [`terraform-google-modules/cloud-storage/google//modules/simple_bucket`](https://registry.terraform.io/modules/terraform-google-modules/cloud-storage/google/latest/submodules/simple_bucket) 12.4.1 |
| `google_service_account` with the roles it has in its project (`google_project_iam_member`), one account per call | [`terraform-google-modules/service-accounts/google//modules/simple-sa`](https://registry.terraform.io/modules/terraform-google-modules/service-accounts/google/latest/submodules/simple-sa) 5.1.0 |
| `google_project_iam_member`: the other grants in a project, including conditional ones, one call per project. A grant whose role or member refers to a resource stays in the root, as the module names each grant after its member | [`terraform-google-modules/iam/google//modules/projects_iam`](https://registry.terraform.io/modules/terraform-google-modules/iam/google/latest/submodules/projects_iam) 8.3.0 |
| `google_kms_key_ring` with its crypto keys, when the keys share their settings; the module keeps keys from being destroyed | [`terraform-google-modules/kms/google`](https://registry.terraform.io/modules/terraform-google-modules/kms/google) 4.2.1 |
| `azurerm_user_assigned_identity` with its federated identity credentials; the module holds `azurerm` below 5 | [`Azure/avm-res-managedidentity-userassignedidentity/azurerm`](https://registry.terraform.io/modules/Azure/avm-res-managedidentity-userassignedidentity/azurerm) 0.5.3 |

Azure Verified Modules' resource group, network security group, virtual network and storage modules manage their resources through `azapi_resource`. Imported into them, azapi plans an update of its own arguments (such as `retry` and `timeouts`) and of body properties the modules set to null, so they can't pass the zero-change check, and those resources stay in the root or a generated local module.

A module may set arguments the provider keeps only in state, such as the iam-role module's `force_detach_policies`. Import can't set those. The plan then updates them in state alone, and the verification gate lists those updates.

A module may use a provider besides the root's: the network module creates networks through `google-beta`, and Azure Verified Modules declare `azapi`. A root that keeps such a call configures that provider like its own (the same project, region and labels setting, or the same subscription and cloud) and pins it as it pins its own.

Each adapter pins an exact module version and is checked against that version's variables and outputs. At import, the registry says whether a module has a newer release: the report names it, and `--modules latest-untested` calls the newest release instead of the tested one (the plan check still decides whether each call is kept). The provider is pinned to its newest release that the module versions accept, counting only the modules whose adapters have a resource to map in the import; when one of them holds it back, the report says which. A nightly job opens an issue when a newer release comes out, saying whether the adapter fits it; `go run ./internal/adapters/cmd/adaptercheck -write`, or the `adapters` workflow run with `write` on a branch, refreshes the interfaces after a bump.

`--modules=local` uses generated local modules only, for example where the module registry can't be reached; `--modules=none` keeps every resource in the root.

## Your own modules

Modules of your own come before the curated ones. List them in the configuration file, each pinned:

```yaml
modules:
  - source: git::https://github.com/acme/terraform-modules.git//bucket?ref=v1.4.0
  - source: app.terraform.io/acme/queue/aws   # a private registry
    version: 2.1.0
  - source: ./modules/role                    # relative to the configuration file
```

infraharvest fetches each module with Terraform, so git and private registry credentials work as they do for Terraform, and matches it by its signature: its resources, one of each type, form the cluster, around the one the others refer to. Where the module sets an argument to `var.<name>`, the call passes the imported value. Where it refers to another of its resources, the imported resource must refer to the matching one. Any other expression is the module's own, and the plan check judges it. A cluster with a setting the module doesn't take is declined, as with the curated modules. A module with `count`, `for_each`, `dynamic` blocks, nested modules or two resources of a type isn't matched yet: the log says so, and the curated modules and generated local modules apply.

## Generated local modules

Other resources that come in clusters, such as a security group and its rules, move into a generated local module when two or more clusters in a root have the same shape. The module goes in `<out>/modules/<kind>_<hash>/` (`main.tf`, `variables.tf`, `outputs.tf`, `README.md`), and each cluster becomes a call to it. Values the clusters share stay in the module, and values that differ become typed variables. References from other resources use the module's outputs, and the import blocks import into the module. Identical modules in different roots are written once. The change is kept only if a new plan shows no extra changes.

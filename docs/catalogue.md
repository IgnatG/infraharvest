# Catalogue

The catalogue is what infraharvest knows about each Terraform resource type, kept as data rather than code:

- the ID Terraform imports a resource with, when it isn't the ID the lister found;
- types Terraform can't import;
- arguments to leave out of generated configuration, and arguments the provider keeps only in state;
- resources the default selection leaves out, such as the ones the cloud creates itself;
- child resources imported with a resource, such as an S3 bucket's versioning;
- the data source that reads a resource the selection leaves out;
- for AWS, where a type's ARNs name its resources (to find their tags), and the CloudFormation type of the types listed through Cloud Control;
- the type a cloud's inventory API lists a resource type as: Azure Resource Graph, Google Cloud Asset Inventory.

It is YAML, one file per provider and service under [`internal/catalogue/data/`](https://github.com/IgnatG/infraharvest/tree/main/internal/catalogue/data), built into the binary. A type the catalogue doesn't mention imports with the ID its lister found, and nothing else changes.

## Format

```yaml
# yaml-language-server: $schema=../../schema.json
provider: aws          # the Terraform provider's local name
service: route_table   # the infraharvest service that lists these types
types:
  aws_route_table:
    data_source: { type: aws_route_table, argument: route_table_id }
  aws_route_table_association:
    # A subnet's association, or a gateway's.
    import_id:
      - "{subnet_id}/{route_table_id}"
      - "{gateway_id}/{route_table_id}"
  aws_main_route_table_association:
    not_importable: Terraform can't import it; the VPC's main_route_table_id records the main route table.
```

The comment on the first line lets editors with the YAML language server check files against [`internal/catalogue/schema.json`](https://github.com/IgnatG/infraharvest/blob/main/internal/catalogue/schema.json) as you type. The relative path works for the built-in files only; in a directory of your own for `--catalogue` (see [Overriding entries](#overriding-entries)), name the schema by its address instead:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/IgnatG/infraharvest/main/internal/catalogue/schema.json
```

| Field | Meaning |
|---|---|
| `service` | The infraharvest service that lists the type, if not the file's. |
| `import_id` | A template, or a list of them: the first whose fields all have values builds the import ID. `{id}` is the ID the lister found and `{<name>}` one of the attributes it recorded. Filters change a field: `{id\|after_last:/}` keeps what follows the last `/`; `before_last`, `trim_prefix` and `trim_suffix` work the same way. When no template has all its fields, the resource is reported as not importable. |
| `not_importable` | Why Terraform can't import the type. Its resources are reported, not imported. |
| `omit` | Arguments to leave out of generated configuration. Each must be optional, and computed or never read back (such as a password AWS doesn't return), so that leaving it out changes no plan. `name=value` leaves an attribute out only where it is set to that value, such as a default the cloud reports that the provider rejects as an argument (`default_encryption_scope=$account-encryption-key`). `a.b` reaches into nested blocks, `*.b` into nested blocks of any type (such as the `tags_all` AWS computes in an instance's `root_block_device`), and conditions after ` when ` narrow where an entry applies (see [Leaving out arguments](#leaving-out-arguments)). Under the type `"*"`, they apply to every type. |
| `defaults` | The values the provider or the cloud gives optional arguments it isn't told, as `path=value`, with a path into nested blocks: `storage_class=STANDARD`, `soft_delete_policy.retention_duration_seconds=604800`. Generated configuration leaves out the arguments set to them, and a block they leave empty, where the plan stays the same, as it does the zero values the provider defaults to. Unlike those, computed arguments go too: the catalogue says the value is the default, so leaving it out hides no setting. |
| `state_only` | Arguments the provider keeps only in state, such as settings it uses when it deletes a resource. An import leaves them unset, so the verification gate accepts a plan that sets them. |
| `keep_empty` | Optional arguments whose empty value (`""`, `[]` or `{}`) the provider documents as a setting of its own, such as one that removes what the cloud adds by default. Generated configuration keeps them where they are set empty; other empty values are left out (see [Tidying](output.md#tidying)). |
| `provider_versions` | What `omit`, `defaults` and `state_only` hold only for some versions of the provider, by a [version constraint](https://developer.hashicorp.com/terraform/language/expressions/version-constraints) on it, such as a default that a major release changed (see [Provider versions](#provider-versions)). |
| `lifecycle` | Lifecycle settings every resource of the type gets, with the `reason`, which goes in a comment above them: `create_before_destroy: true`, such as for an API Gateway deployment, which a stage refers to while it is replaced. They change how Terraform replaces a resource, never the plan of an import. |
| `skip` | Rules for resources the default selection leaves out, each with a `reason` and conditions that must all hold: `ids`, `id_prefixes`, `attributes` (values the lister recorded), `attribute_prefixes` (values that start with one of these), `attribute_suffixes` (values that end with one of these), or `all: true` for every resource of the type. `ignore_case: true` compares them whatever the case, as Azure names are. Rules under the type `"*"` apply to every type, after the type's own. A selection file can still include them. |
| `children` | Types of the separate resources that hold parts of a resource's configuration. The provider checks which a resource has and imports those with it. |
| `data_source` | The data source that reads one resource by its import ID, and the argument that takes the ID. When the selection leaves a resource out, configuration that refers to it reads it through this. |
| `arn` | AWS only: the ARN's `service` and `resource_type`, for types whose IDs aren't ARNs, so tags can be matched to resources. |
| `cloud_control` | AWS only: the CloudFormation `type` and the `identifier` property of a type listed through the Cloud Control API. The identifier is the import ID. |
| `inventory` | The `type` a cloud's inventory API lists resources of the type as, and, when that type covers several Terraform types, the `name` pattern of this one's resources (see [Inventory types](#inventory-types)). |

What needs the cloud to decide, such as whether a VPC is the default one or a CloudFormation stack manages a resource, stays in the provider's code.

## Leaving out arguments

Terraform writes every argument a resource has, so generated configuration repeats what the cloud gives a resource that doesn't set it. `defaults` names such values, and the engine leaves them out where the plan stays the same. `omit` says what `defaults` can't: arguments to leave out only where conditions hold, blocks whatever they hold, and values that depend on the resource or on what the cloud alone can tell, such as a default policy or an instance type's CPU options. Each must be optional, and computed or never read back, so that leaving it out changes no plan.

```yaml
types:
  aws_lb_target_group:
    omit:
      - stickiness when stickiness.enabled=false         # a block, where its own argument holds
  aws_instance:
    omit:
      - root_block_device.iops=3000 when root_block_device.volume_type=gp3   # in nested blocks
      - availability_zone when subnet_id                 # where another argument is set
      - cpu_options when @cpu_options=default            # where the lister recorded a value
  aws_sns_topic:
    omit:
      - 'policy={"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[...]}'
```

- An entry is a path, `name` or `a.b.c` through nested blocks, optionally with `=value`, then optionally ` when ` and conditions joined by ` and `. Without a value, the argument or block goes whatever it holds.
- A condition is `path` (set and not null), `path=value`, or `!path` (not set, or not at that value with `!path=value`). A condition whose path starts with the blocks the argument is in refers to those same blocks; any other refers to the resource, where it holds if it holds in any of the blocks it names.
- `@attribute=value`, `@attribute` (set and not empty) and `!@attribute` are conditions on what the lister recorded for the resource, for what only the cloud can tell, such as whether an instance's CPU options are its type's defaults.
- A value is compared as a string (`true`, `3`, `text`). `{*}` in it stands for any text, and `{argument}` for the value of one of the resource's string arguments: `cache_namespace={resource_id}`. A value that is a JSON object or array is compared as JSON with the argument's value, such as a policy, with arrays in any order.
- Whether an entry applies is decided on the resource as Terraform generated it, before anything is left out. The verification gate's standards check fails a resource that still sets an argument where an entry applies.

## Provider versions

Some of what the catalogue knows holds only for some versions of the provider: a default that a major release changed, or an argument it renamed. `provider_versions` holds those `omit`, `defaults` and `state_only` entries, under a version constraint on the provider:

```yaml
provider: azurerm
types:
  azurerm_storage_account:
    provider_versions:
      "< 5.0":
        defaults: [allow_nested_items_to_be_public=true]
      ">= 5.0":
        defaults: [allow_nested_items_to_be_public=false]
```

An import applies the entries whose constraint accepts the provider version it pins (`~> 4.81` pins 4.81 and later 4.x releases, so `< 5.0` applies), after the entry's own; `infraharvest verify` applies those that accept the version in the root's lock file. Constraints take the operators Terraform's do (`=`, `!=`, `>`, `>=`, `<`, `<=`, `~>`, joined with commas). An entry under a constraint must add something, and not repeat what the entry has for every version.

Defaults of optional arguments that the provider doesn't compute need no entry: infraharvest finds them by plan (see [Tidying](output.md#tidying)). What needs a `defaults` entry is a default the cloud sets for an argument the provider computes, which a plan can't tell from a setting. The entry for every type, `"*"`, takes no `defaults`.

## Inventory types

The services that list a whole subscription or project through the cloud's inventory API, `resource_graph` for Azure and `cloud_asset` for Google, map what it lists to Terraform types through `inventory` entries:

```yaml
provider: google
types:
  google_compute_disk:
    inventory:
      type: compute.googleapis.com/Disk
      name: //compute.googleapis.com/projects/*/zones/*/disks/*
  google_compute_region_disk:
    inventory:
      type: compute.googleapis.com/Disk
      name: //compute.googleapis.com/projects/*/regions/*/disks/*
```

`type` is the inventory API's type: a Cloud Asset Inventory asset type, or an Azure resource type such as `Microsoft.Network/networkWatchers`. When one type covers several Terraform types, as Cloud Asset Inventory's `compute.googleapis.com/Disk` covers zonal and regional disks, `name` tells them apart: a pattern of the resource's full name (Google) or ID (Azure), in which `*` stands for one segment between slashes. Both match whatever the case. The catalogue is checked so that no two entries can claim the same resource.

For Google, the catalogue is the whole mapping: what has no entry isn't imported. For Azure, [aztft](https://github.com/magodo/aztft) maps every type it knows, and an `inventory` entry overrides it, for a type aztft doesn't know or maps to a type you don't want.

## Overriding entries

`--catalogue <directory>` on `import`, `discover` and `verify` reads the `.yaml` and `.yml` files in the directory on top of the built-in catalogue. An entry for a type the catalogue already has replaces only the fields it sets, so `skip: []` drops the built-in skip rules and `import_id` replaces the templates. Entries for new types add to it. Files for the other providers (`aws`, `azurerm` and `google`) are ignored; a file with any other `provider:`, or a directory without catalogue files, is an error. Use this to fix an import ID or add a skip rule without waiting for a release; please also open an issue or a pull request, so everyone gets the fix.

```yaml
# my-catalogue/aws.yaml
provider: aws
types:
  aws_iam_role:
    skip:
      - reason: managed by the platform team's stack
        id_prefixes: [platform-]
```

## Golden tests

Each entry that changes what an import does (an import ID, a skip rule, children, a data source, arguments to omit or that only state holds, a placeholder, lifecycle settings, or `not_importable`) has a golden test in [`internal/catalogue/testdata/`](https://github.com/IgnatG/infraharvest/tree/main/internal/catalogue/testdata). So do these AWS and Google network, DNS, identity and key types, whatever their entries hold: `aws_vpc`, `aws_subnet`, `aws_internet_gateway`, `aws_nat_gateway`, `aws_route_table`, `aws_route_table_association`, `aws_main_route_table_association`, `aws_vpc_endpoint`, `aws_security_group`, `aws_security_group_rule`, `aws_vpc_security_group_ingress_rule`, `aws_vpc_security_group_egress_rule`, `aws_iam_role`, `aws_iam_policy`, `aws_iam_instance_profile`, `aws_iam_role_policy`, `aws_iam_role_policy_attachment`, `aws_route53_zone`, `aws_route53_record`, `aws_kms_key`, `aws_kms_alias`, `google_compute_network`, `google_compute_subnetwork`, `google_compute_firewall`, `google_compute_route`, `google_dns_managed_zone`, `google_service_account`, `google_project_iam_custom_role`, `google_kms_key_ring`, `google_kms_crypto_key`. The files are:

- `<provider>/<type>.json` holds resources as a lister records them: `id`, `name` and `attributes`;
- `<provider>/<type>.golden.tf` holds what the catalogue makes of them: the import blocks, the resources left out or not importable and why, the data sources, the children, and the arguments left out;
- an optional `<provider>/<type>.generated.tf`, as `terraform plan -generate-config-out` wrote it, is shown without the arguments the catalogue omits.

`go test ./internal/catalogue` fails when a golden file differs, when an entry that needs a golden test has none, or when a file breaks the format or the schema. After changing an entry, rewrite the golden files and review the difference:

```sh
go test ./internal/catalogue -run TestGolden -update
```

Without a local Go toolchain, push the branch as `golden/<anything>` (or run `gh workflow run remote --ref <branch> -f task=golden`) and download what the run rewrote, from the repository's root, with `gh run download <run-id> -n golden`.

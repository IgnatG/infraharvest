# Migrating from Terraformer

infraharvest started as a fork of [Terraformer](https://github.com/GoogleCloudPlatform/terraformer). The Terraformer way of importing still works as the `legacy` engine. The recommended way is `--engine=terraform` (or `tofu`). It imports with Terraform's own `import` blocks and `terraform plan -generate-config-out`, and checks that the result plans with no changes. This page maps Terraformer's habits onto it.

## What changes

| | Terraformer (`--engine=legacy`) | infraharvest (`--engine=terraform` or `tofu`) |
|---|---|---|
| Output | HCL from Terraformer's own printer, and a `terraform.tfstate` | `import` blocks plus configuration that Terraform generates. No state is written: `terraform apply` records the resources, and changes nothing in the cloud |
| Check | None | The verification gate runs on every root: format, validate, a plan that only imports, the output standard, scanners, secrets, determinism |
| Terraform and providers | You install the provider plugins | infraharvest finds or downloads Terraform (or uses OpenTofu), and pins Terraform and the provider in `versions.tf` |
| Resource names | `tfer--<id>` | snake_case labels taken from names, unique per type |
| Layout | `{output}/{provider}/{service}/` | One root per state boundary: `{output}/{provider}/{account}/{region}/` |
| What gets imported | Everything `--resources` lists | What a selection file includes, or `--all`: the default selection, which leaves out what the cloud manages itself (default VPCs, service-linked roles, ...) |
| Secrets | Written into state | Become `sensitive` variables with no default |

## Flags

| Terraformer flag | With `--engine=terraform` |
|---|---|
| `--resources`, `--excludes`, `--regions`, `--profile`, `--filter` | The same |
| `--connect` (`terraform_remote_state` between services) | Not needed. A root holds every service of one account and region, and resources refer to each other directly (`vpc_id = aws_vpc.main.id`). Resources the selection leaves out are read through `data` sources |
| `--path-pattern` | Still accepted, with `{account}` and `{region}` |
| `--state=bucket`, `--bucket` | A `backend:` section in the configuration file (`--config`). Each root gets a `backend.tf` with its own state key. S3 locks with `use_lockfile`, never DynamoDB. `infraharvest bootstrap` writes a root that creates the bucket |
| `--compact` | Not needed: each root has one `generated.tf` |
| `--output json` | Prints the import report as JSON |
| `--retry-number`, `--retry-sleep-ms` | Not used. The AWS SDK retries throttled calls in adaptive mode, and `--list-timeout` bounds each service |
| `--plan`, `infraharvest plan` | Legacy only. To check generated roots again, use `infraharvest verify` |
| — | New: `--selection`, `--all`, `--managed-state`, `--modules`, `--allow-partial`, `--config` |

## A typical migration

1. **List what there is**, and review what will be imported:

   ```sh
   infraharvest discover aws --resources=vpc,subnet,sg,s3,iam --regions=eu-west-2 --selection=selection.yaml
   ```

   Edit `selection.yaml`, or add rules to it, to include or exclude resources.

2. **Leave out what Terraform already manages.** If you imported with Terraformer before, point `--managed-state` at that state; infraharvest reads Terraformer's version 3 state too. Those resources are then left out, so they aren't imported twice:

   ```sh
   infraharvest import aws --engine=terraform --selection=selection.yaml \
     --managed-state=./generated-terraformer --config=infraharvest.yaml
   ```

3. **Review the output.** Each root's `README.md` lists what was imported, the secret variables to set, and the checks. `report/report.md` covers the whole import.

4. **Take the roots under management.** In each root, set the secret variables, then run `terraform init` and `terraform plan`. Every resource should plan as an import with no changes. Then run `terraform apply` and delete `imports.tf`.

5. **Check again after edits** with `infraharvest verify`.

## Configuration file

Flags you used to repeat go in `infraharvest.yaml`:

```yaml
version: 1
settings:
  engine: terraform
  selection: selection.yaml
providers:
  aws:
    profile: prod
    regions: [eu-west-2, us-east-1]
    resources: [vpc, subnet, sg, s3, iam]
backend:
  s3:
    bucket: acme-terraform-state
    region: eu-west-2
    key_prefix: imported
```

Flags on the command line take precedence over the file.

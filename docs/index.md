# infraharvest

infraharvest generates Terraform configuration for infrastructure that already exists. It lists your resources, writes an `import` block for each, and has Terraform (or OpenTofu) generate the configuration with `plan -generate-config-out`. It then tidies that configuration, and checks that it plans with no changes.

[Quickstart](quickstart.md){ .md-button .md-button--primary } [Install](install.md){ .md-button }

## What you get

- **Roots that plan with no changes.** Every change infraharvest makes to the generated configuration is kept only if a new plan shows no extra changes, and every root goes through a verification gate: format, validate, a zero-change plan, the output standard, scanners, secrets and determinism.
- **Configuration people would write.** References instead of hard-coded IDs, shared tags as a local, secrets as variables, and clusters of resources as calls of curated public modules, such as terraform-aws-modules or terraform-google-modules, or of your own.
- **Only what you choose.** `discover` lists what is there into a selection file; you review it, and `import` brings in what it includes. The default selection leaves out what the cloud creates on its own and what another tool already manages.
- **An audit first.** `discover`, with `--managed-state`, shows what in an account no Terraform state or other tool manages, without importing or applying anything (see [Audit](audit.md)).
- **Read-only.** infraharvest reads the cloud and writes files. It never applies anything and writes no state; `terraform apply` on a generated root records the resources in state without changing them.

## How it works

1. The provider's listers call the cloud's APIs and record each resource's type, ID and attributes. The [catalogue](catalogue.md) turns them into the ID Terraform imports the resource by, and says what the default selection leaves out.
2. The selection decides what to import (see [Usage](usage.md)).
3. infraharvest writes an `import` block for each resource into a root, one per account (or subscription, or project) and region, and runs `terraform plan -generate-config-out`, so the provider itself reads every resource.
4. It tidies the generated configuration (see [Output](output.md) and [Modules](modules.md)).
5. It runs the verification gate on every root and writes a report.

AWS, Azure and Google Cloud are the supported providers: inventory APIs, the catalogue, curated modules and end-to-end tests on an emulator for each.

## infraharvest and Terraform's own import

Terraform 1.14 and later can find resources itself: [`terraform query`](https://developer.hashicorp.com/terraform/cli/commands/query) runs the `list` blocks of a `.tfquery.hcl` file against the resource types whose provider has a list resource, and with `-generate-config-out` writes an `import` and a `resource` block for each result. HCP Terraform's Search & Import puts a UI on this and, in preview, shows policy results before the import. Both write each resource's arguments as literal values, as `plan -generate-config-out` does.

| Use | When |
|---|---|
| `terraform query`, or Search & Import | A handful of resources, brought into a workspace you already run in HCP Terraform or with Terraform |
| infraharvest | Whole accounts, subscriptions and projects, on Terraform or OpenTofu, run locally: references between resources, locals, modules, secrets as variables, a plan with no changes for every root, environments sharing one root, and [drift](drift.md) accepted into the code |

infraharvest is licensed under [AGPL-3.0-only](https://github.com/IgnatG/infraharvest/blob/main/LICENSE).

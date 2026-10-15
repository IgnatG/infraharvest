# Output

## Layout

By default each root is one state boundary: `<out>/<provider>/<account>/<region>/`, with `global` for global services such as IAM. Azure uses the subscription and the resource group instead (`all` for the whole subscription), and Google Cloud the project and the region (`global` for what belongs to no region); see each cloud's page. `--layout` can change that, with `{account}` and `{region}` as well as `{out}`, `{provider}` and `{service}`.

With `--env-tags=Environment` (any tag or label keys, first match wins), each environment gets roots of its own, with their own state: `<out>/envs/<env>/<provider>/<account>/<region>/`, or wherever `{env}` is in `--layout`. A resource without the tag, such as a bucket's versioning or a subnet's route table association, goes with the tagged resource whose ID its import ID contains, a child import with its parent, and anything else to `shared`. References to what another root manages read it through a data source, where its type has one. Identical shapes in different environments share one generated local module, called from each root. Environments are opt-in: an earlier import's roots keep their paths, so `--incremental` never generates a resource twice.

## Files

Each output directory gets:

| File | Contents |
|---|---|
| `versions.tf` | `required_version` within the major release of the Terraform that generated it (`>= 1.16, < 2.0`), and the provider pinned to its newest minor release line (`~> 6.67`) |
| `providers.tf` | The provider configuration |
| `imports.tf` | One `import` block per resource. Delete it after the first `terraform apply` |
| `network.tf`, `s3.tf`, `iam.tf`, ... | The configuration Terraform generated for the resources, a file per service: the network's resources (VPCs, subnets, route tables, gateways, security groups) together in `network.tf`, the others in their service's file. A module call goes with the first resource it took |
| `variables.tf` | Only if there are secrets: a `sensitive` variable, with no default, for each secret value. Terraform doesn't write secret values (an SSM parameter's `value`, for example) into the configuration it generates. Set the variables before planning, for example in a `.tfvars` file kept out of version control. Write-only arguments (`*_wo`) stay unset |
| `.terraform.lock.hcl` | The provider version, the same for every directory of one import |
| `rejected.hcl` | Only if some resources couldn't be imported: their `import` block and generated configuration, under the errors Terraform reported. Terraform doesn't load this file. Fix a resource and move its import block into `imports.tf` and its resource into the configuration, or leave it out. Rejected resources make the import exit non-zero unless `--allow-partial` is set |

Each directory also gets a `README.md` with what was imported and the steps left to take, and the output directory gets a `.gitignore` for state, plans, `.terraform/` and `.tfvars` files, unless it already has one. Resource names are snake_case labels made from the names the resources have in the cloud: on AWS, the `Name` tag where a resource has one (`aws_subnet.shop_private_eu_west_2a`), else the name or ID the lister found.

## Tidying

Terraform writes every optional argument into the configuration it generates, so infraharvest leaves out the ones that only repeat a default. That means arguments set to `null`, optional arguments set to `false`, `0`, `""`, `[]` or `{}` when that is the provider's default, and nested blocks left with nothing set (`ttl {}`). Only the plan can tell a default from a setting, so infraharvest plans without these arguments and puts back any whose absence would change a resource. Computed arguments keep their values: leaving one out never shows in the plan, so it could hide a real setting.

Literals that are another imported resource's ID or ARN become references: `vpc_id = aws_vpc.main.id`, `role_arn = aws_iam_role.app.arn`. An argument named after a resource type refers to that resource by name, for example `bucket = aws_s3_bucket.logs.bucket`. Values several resources share are skipped, unless one is the others' parent (a bucket and its configuration resources). So are references that would make resources depend on each other in a loop. The references are kept only if a new plan shows no extra changes.

A resource the import listed but the selection left out, such as a default VPC or a default security group, is read through a data source instead: `security_groups = [aws_security_group.web.id, data.aws_security_group.sg_0abc1234.id]`, with the `data` blocks in `data.tf`. This only happens when the resource type has a data source that reads it by its ID, and the result is checked by plan in the same way.

Repeated values move into `locals.tf`:

- **Shared tags:** tags every resource in a directory shares become `local.tags`. On AWS they are applied through the provider's `default_tags`, and each resource keeps only its other tags. Because AWS records every tag in `tags_all`, the move is kept only if a new plan shows no extra changes. On Google Cloud, labels every resource shares become `local.labels`, applied through the provider's `default_labels`; `goog-` labels stay on the resources. On Azure, where azurerm has no such setting, each resource and module call sets `tags = local.tags`, or `merge(local.tags, { ... })` with its other tags; `hidden-` tags stay on the resources.
- **Repeated identifiers:** IDs and ARNs used three or more times (`vpc_id = "vpc-0abc1234"`) become locals named after the argument that uses them (`local.vpc_id`).

AWS resources don't repeat `region` (the provider's) or the computed `tags_all`.

Clusters of resources move into module calls: see [Modules](modules.md).

S3 buckets are imported split, as the AWS provider recommends: each part of a bucket's configuration it has (versioning, encryption, lifecycle, CORS, website, logging, public access block, ownership controls, transfer acceleration, requester pays, object lock, replication, policy) is its own resource, and the bucket's deprecated inline arguments are left out. ACLs aren't imported yet.

Security group rules are imported as `aws_vpc_security_group_ingress_rule` and `aws_vpc_security_group_egress_rule` resources of their own, by their `sgr-` IDs, as the AWS provider recommends; the groups leave out their inline `ingress` and `egress`.

Some configuration can't be read back from the cloud. A Lambda function deployed from a zip package gets `filename = "lambda/<function>.zip"` as a placeholder, which `lifecycle { ignore_changes = [filename, source_code_hash] }` tells Terraform to ignore: Terraform manages the function's settings, and the code stays deployed as it is. A comment in the block says how to have Terraform deploy the code instead. An import whose resource Terraform finds gone, such as a KMS key scheduled for deletion, is left out with that reason.

Generated local modules declare the attributes of their object variables `optional()`, so their calls set only the attributes that have values.

## Report

The `report/` directory of the output records the import:

| File | Contents |
|---|---|
| `coverage.json` | What the listers found, by type and directory, and what became of it: imported, left out (with Terraform's errors), not importable, or lost to a failed directory or service. Also the secret variables to set |
| `manifest.json` | The versions used: infraharvest, Terraform or OpenTofu, and the provider (constraint and locked version) |
| `report.md` | The same, for people |

With `--report-tags owner,team`, the report also counts what was discovered, imported and managed by the value of each tag key, including resources without the tag, so you can see which owners still have resources outside infrastructure as code. It counts the resources whose tags discover could read (see Choosing what to import). Reports hold no timestamps or absolute paths, so importing an unchanged estate produces the same files. With `--format json`, the whole report is also printed to stdout as one JSON document (`schema_version` 1); logs always go to stderr.

## Verification gate

After generating a directory, infraharvest runs its verification gate on it and records the results in the directory's README and in `coverage.json`:

| Check | Passes when |
|---|---|
| G1 format | `terraform fmt` has nothing to change |
| G2 validate | `terraform validate` passes |
| G3 plan | The plan imports every resource and changes nothing else. Secret variables get placeholder values for this plan, so the arguments they set may change, and so may arguments the provider keeps only in state (for example a Secrets Manager secret's `recovery_window_in_days`) |
| G4 standards | The directory, and the local modules it calls, follow the output standard. Terraform and providers are pinned (`required_version` within one major release, providers with `~>`). Registry modules have an exact version, and git modules a `?ref=`. Variables and outputs are typed and described, and no sensitive variable has a default. There is no `"${...}"` around a single expression, and no argument the provider leaves out (such as an S3 bucket's deprecated inline settings). There are no state files and only `.tf` files. Local modules have `main.tf`, `variables.tf`, `outputs.tf`, `versions.tf` and a README, and no `examples/` |
| G5 scanners | The scanners installed on `PATH` run on the directory. tflint must find nothing at warning level or above, since its findings concern the generated code. trivy and checkov findings are listed but never fail the check: they describe the infrastructure's own settings, which the configuration must mirror to plan with no changes |
| G6 secrets | No written file contains a value the provider marks sensitive (unless another attribute holds the same value in the open, such as a parameter that holds its region), or a credential such as a private key, or an AWS access key ID with its secret access key (an ID alone, such as an IAM access key's, is an identifier) |
| G7 determinism | The configuration calls no function whose result changes between runs, such as `timestamp()` |

A failed check counts like a resource that couldn't be imported (exit code 1, or 3 with `--allow-partial`).

To check generated roots again, for example after editing them, run `infraharvest verify [output-directory]`. It runs the same checks on every root, initialising each without its backend, so it needs no access to the state; set secret variables first. `infraharvest report [output-directory]` prints an import's report again (`--format json` for JSON).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Everything found was imported (types Terraform can't import aside) |
| 1 | Something couldn't be imported, and `--allow-partial` isn't set |
| 2 | The import couldn't run, for example without credentials or a Terraform binary |
| 3 | Something couldn't be imported, `--allow-partial` is set, and the output has the rest |

## Repairs

When the configuration Terraform generates doesn't validate, infraharvest fixes what Terraform rejects where that doesn't change its meaning, then plans again. It removes arguments that are unset in effect (zero values), arguments that duplicate another one (`subnets` next to `subnet_mapping` blocks), and nested blocks whose arguments are all null. Inside objects, it writes `null` for unset strings that Terraform generated as `""`.

# Configuration

`--config infraharvest.yaml` sets any flag the command line doesn't, and the state backend of the generated roots:

```yaml
version: 1
settings:                # flags of every provider command
  selection: selection.yaml
providers:               # flags of one provider command
  aws:
    profile: prod
    regions: [eu-west-2, us-east-1]
    services: [vpc, subnet, sg, s3]
backend:                 # one of s3, azurerm, gcs
  s3:
    bucket: acme-terraform-state
    region: eu-west-2
    key_prefix: imported
modules:                 # your own modules, before the curated ones (see Modules)
  - source: git::https://github.com/acme/terraform-modules.git//bucket?ref=v1.4.0
environments:            # roots per environment (see Environments below)
  dev:
    aws: { accounts: ["111111111111"] }
  prod:
    aws: { accounts: ["222222222222"] }
```

A setting is named after its flag, without the leading dashes, as in `managed-state: backend` or `list-parallel: 8`. The keys under `providers` are the commands, `aws`, `azure` and `google`; any other, such as `azurerm`, is an error. A list gives a flag several values: comma-separated for `regions` and `services`, and one filter per item for `id`, `tag` and `where`, which the command line takes once per filter, as in `id: ["vpc=vpc-1,vpc-2", subnet=subnet-1]`.

Each root gets a `backend.tf` with its own state key, made of the prefix and the root's path under `--out` (`imported/aws/<account>/<region>/terraform.tfstate` above). The backends take:

| Backend | Required | Optional | State of each root |
|---|---|---|---|
| `s3` | `bucket`, `region` | `key_prefix`, `kms_key_id` | `key = "<key_prefix>/<root path>/terraform.tfstate"`, encrypted |
| `azurerm` | `resource_group_name`, `storage_account_name`, `container_name` | `key_prefix` | `key = "<key_prefix>/<root path>/terraform.tfstate"` |
| `gcs` | `bucket` | `prefix` | `prefix = "<prefix>/<root path>"` |

S3 state is locked with S3's own lock file (`use_lockfile = true`), not DynamoDB. infraharvest writes `backend.tf` after checking the root, so importing never needs access to the state bucket. Without a `backend`, the roots get no `backend.tf` and keep local state (see [Applying the roots](output.md#applying-the-roots)). `--managed-state=backend` reads the state of the backend configured here, so that an import run again leaves out what the roots already manage (see [Usage](usage.md#choosing-what-to-import)).

If the storage doesn't exist yet, `infraharvest bootstrap --config infraharvest.yaml` writes a root into `<out>/bootstrap` that creates it: an S3 bucket, an Azure storage account and container, or a Cloud Storage bucket. The storage is versioned, encrypted, not public, reachable only over TLS, and protected from `terraform destroy`. Apply it once, with rights to create storage, before planning the generated roots.

## Environments

`environments` names your environments and where each one is. Each environment gets roots of its own, with their own state, where `--env-tags` puts them: `<out>/envs/<env>/<provider>/<account>/<region>/`, or wherever `{env}` is in `--layout` (see [Layout](output.md#layout)).

```yaml
environments:
  dev:
    aws: { accounts: ["111111111111"] }
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000001"] }
    google: { projects: [shop-dev] }
  staging:
    aws: { accounts: ["333333333333"] }
    tags: { Environment: staging }
  prod:
    aws: { accounts: ["222222222222", "333333333333"] }
    tags: { Environment: prod }
    tokens: [prd]
```

| Key | What it is |
|---|---|
| `aws.accounts`, `azure.subscriptions`, `google.projects` | The environment's AWS accounts, Azure subscriptions and Google Cloud projects, by ID |
| `tags` | Tags (labels on Google Cloud) every resource of the environment has, compared as `--env-tags` compares environments: in lower case, with spaces and other characters made dashes, so `Prod` and `prod` are the same |
| `tokens` | The words that stand for the environment in resource names, such as `prd`; by default the environment's name. They match in any case, so `prd`, `Prd` and `PRD` are the same token. Merging (see [Merging environments](#merging-environments)) takes them out of names, and `var.env`, in `variables.tf` and each environment's var file, is set to the one the names use most, as they write it: `PRD` where the names say `shop-PRD-orders` |

Each resource is placed by where it was listed first, then by its tags:

- An account, subscription or project one environment names is all that environment's, whatever the resources' tags.
- One that several environments name, such as `333333333333` above, is split by their tags: a resource goes to the environment whose tags it has. One without them goes as with `--env-tags`: with the resource whose ID its import ID contains, such as a bucket's versioning, a child import with its parent, and anything else to `shared`. Each of these environments needs `tags`, with a key in common whose values differ, so that no resource can be in two; otherwise the file is rejected.
- An environment that names no account, subscription or project is found by its tags alone, in the accounts, subscriptions and projects no environment names, and those environments' tags must tell them apart too.
- An account, subscription or project no environment names, and none found by tags alone, keeps the layout without environments. Where the import can't tell the account, subscription or project, such as when the AWS credentials can't say which account they are in, only the environments found by tags alone apply, and the import warns, in the log and the report (`warnings` in `coverage.json`), that the environments that name an account, subscription or project don't.

An environment's name is a directory's: lower-case letters, digits, dots, dashes and underscores, and not `shared`. With `--env-tags`, the directory is the tag's value in lower case, with other characters made dashes, so resources tagged `Prod`, `PROD` and `prod` share `envs/prod/`. `environments` doesn't go with `--env-tags`, which places resources in environments by itself, and a `--layout` of your own needs `{env}`.

A root an earlier import generated into the same `--out` keeps its directory, as its state key comes from its path: the resources the environments would now put under `envs/<env>/` stay in it, with `--regenerate` and `--incremental` too, and the report names each such root. To lay an estate out by environment from the start, import it into a new `--out`.

### Merging environments

With `environments:`, the roots each stack has in its environments merge into one root they share, laid out as hand-written multi-environment code usually is: one set of `.tf` files, and what differs between the environments in var files. A stack is the roots of one provider, region and `{service}`, if the layout has it: `<out>/envs/dev/aws/111111111111/eu-west-2/` and `<out>/envs/prod/aws/222222222222/eu-west-2/` merge into `<out>/aws/eu-west-2/`. With a `--layout` of your own, the merged root's directory is the layout without its segments that hold `{env}`, `{account}`, `{subscription}` or `{project}`. Set `merge-environments: false` under `settings`, or pass `--merge-environments=false`, to keep a root per environment; `--merge-environments` without `environments:` is an error.

Merging runs once every account, subscription and project of the command is imported, as a stack's environments are usually in different ones. It matches each environment's resources by type and import ID, then by label, then by the matched resources they refer to, with the environments' tokens, the account, subscription or project ID and the AWS region taken out of names and IDs first, so `shop-prd-orders` matches `shop-dev-orders`. What differs is written as:

| What differs | In the merged root |
|---|---|
| A name holding the environment's token | A named local in `locals.tf`, from `naming_prefix = "<workload>-${var.env}"`: `orders_name = "${local.naming_prefix}-orders"` |
| The account, subscription or project ID, or the region, in a value | `data.aws_caller_identity`, `data.aws_region`, `data.azurerm_client_config` or `data.google_project`, through locals |
| A boolean, or an enum-like value, one environment alone has | A condition on the environment: `force_destroy = var.env == "dev"` |
| Any other value | A variable, typed from the provider's schema and described, set in each environment's `terraform.tfvars`; with three environments or more, the value a strict majority has is its default |
| Resources some environments only have | `count = var.deploy_<feature> ? 1 : 0`, one `deploy_` variable per feature: resources that refer to one another, or whose names start with the same word |
| A data source some environments only read | The `count` of the feature that alone reads it, referred to as `data.<type>.<name>[0]`: only the feature's resources, or data sources it counts, refer to it, in the same environments |
| Resources of one type and shape, a different number in each environment | One resource with `for_each` over a map variable, keyed by their names |
| Three or more resources of one type and shape that every environment has | One resource with `for_each`, keyed by their names: over a local map in `locals.tf` where every environment's are the same, their names from `local.naming_prefix`, else over a map variable each environment sets. References to them are to their keys, as `aws_sqs_queue.this["orders"].url`; a resource another of them refers to stays a resource of its own, as a resource's instances can't refer to one another |
| A resource whose arguments mostly differ, or whose plan would change (see below) | Residue: each environment's resource as its root had it, counted by a `deploy_` variable of its own, its name a named local beside the others: from `local.naming_prefix` where it holds the environment's token, as it is otherwise |
| A module call | One call, matched by name, its differing arguments variables, counted by a `deploy_` variable where some environments lack it; a call of another source or release in one environment is residue (see [Modules](modules.md#merged-environments)) |

The merged root has:

| File | Contents |
|---|---|
| `providers.tf` | `required_version`, the provider requirement, an empty `backend "<type>" {}` where the roots had a backend, and the provider, with `region = var.aws_region`, and `default_tags` (Google: `default_labels`) with `Environment = var.env` where a tag holds the environment's token. No role: each environment plans and applies with its own credentials |
| `variables.tf` | `env`, with a `validation` of the environments' tokens, then every variable, typed, described, with a default where an environment may leave it out; secrets `sensitive`, never set in a var file; `import_ids` |
| `locals.tf`, `data.tf` | The naming prefix and the names, the account and region from data sources, and the data sources the roots read |
| `network.tf`, `s3.tf`, ... | The resources and module calls, in the files their roots had them in |
| `imports.tf` | An `import` block for each resource, its ID from `var.import_ids`; for one some environments lack, with `for_each`, so that each environment imports what it has |
| `vars/common.tfvars` | Values every environment shares that the defaults don't give |
| `vars/<env>/terraform.tfvars` | `env`, then the values the environment sets, grouped by the file of what they are for, its `deploy_` variables first |
| `vars/<env>/imports.tfvars` | The environment's import IDs, by address. Delete it, with `imports.tf`, after every environment's first apply |
| `vars/<env>/backend.hcl` | The environment's backend settings, where the roots had a backend, with the state key its root had |
| `README.md` | The environments, and how to plan and apply each |

Plan and apply each environment as the README says, with that environment's credentials:

```sh
terraform init -reconfigure -backend-config=vars/prod/backend.hcl
terraform plan -var-file=vars/common.tfvars -var-file=vars/prod/terraform.tfvars -var-file=vars/prod/imports.tfvars
```

Without a backend, each environment keeps its state in a workspace named after it: `terraform workspace select -or-create prod` after `terraform init`.

The merged root has its verification gate too, once per environment, with that environment's credentials and var files (on AWS, the role the environment's root assumed, given to the plan alone through an override file removed after it): each environment's plan must import what its own root imported, and change nothing else. Where a plan changes a resource the environments share, that resource becomes each environment's residue, and every environment plans again, up to three merges. Where the roots can't share a root at all (different providers, or different major releases of the provider; a data source only some environments read, unless one feature alone refers to it; outputs or `moves.tf` of their own), or a plan still changes something, the environments keep their roots as they were generated, and the report says why, under Environments kept apart (`merges` in `coverage.json`, with `merged: false` and a `reason`). Roots that pin different release lines of one provider, as when a module holds one back, merge on the newest of them, with that root's lock file, which every environment's plan then checks.

The roots a stack merged are kept under `<out>/.infraharvest/premerge/`, and `<out>/.infraharvest/merge/` records each merge, so that later runs go on from there:

- `--resume` keeps a merged root whose environments' roots are unchanged, and, where they have changed, merges again from the kept roots, without generating them again.
- `--incremental` adds what is new to each environment's kept root, then merges again, with what is new its environment's residue, so that what the merged root has keeps its addresses; where it wouldn't, the merged root is left as it was, and the run fails, naming what waits. A run with only some of the stack's environments merges the others again from their kept roots, as they were, but can't plan the merged root as them without their credentials: the report says which weren't planned (`unplanned` in `coverage.json`), and `infraharvest verify --environment` plans each, with its credentials, before you apply it. The next run that has them plans them again. Resources no longer in the cloud are reported at their addresses in the merged root.
- A plain import refuses a root its stack merged, which may be in use; `--regenerate` generates both again. Where that gives what the merged root has other addresses, such as resources that now share a `for_each`, `moves.tf` gets a `moved` block for each, from its address before, which the README lists: each environment's next plan moves them in its state, and destroys nothing. Delete `moves.tf` once every environment has been applied. Without `--regenerate`, a merge that would give them other addresses leaves the merged root as it was, and the report says why.
- `infraharvest verify` plans a merged root once per environment, with its var files and the credentials verify runs with; `--environment` names the environments to plan.
- `infraharvest drift` checks a merged root once per environment, with its backend settings and var files; `--environment` names the environments to check. A change accepted for an environment is written into its `vars/<env>/terraform.tfvars` where a variable sets the value, and never into the code the environments share (see [Drift](drift.md#roots-environments-share)).

An environment the merged root doesn't have yet, such as one added to `environments:` since, joins it in the run that imports it: the stack merges again with every environment, those the run doesn't have from their kept roots, unplanned, and the new environment's resources are matched with the others'. Where that fails, or would give what the merged root has other addresses, the merged root stays as it was for the other environments, merged again without the new one where the run has something new of them, and the new environment keeps roots of its own, which the report names with why (`apart` and `apart_reason` in `coverage.json`); a later run that imports it tries again. The roots of an earlier import without environments, which keep their directories, are never merged.

## Behind a TLS-intercepting proxy

If a proxy re-signs HTTPS traffic with its own certificate authority, point `SSL_CERT_FILE` at a PEM file of the certificates to trust (that authority's and the public ones), or `SSL_CERT_DIR` at a directory of them. infraharvest then trusts those certificates instead of the system's, on Windows and macOS as well as on Linux. Terraform and the providers are separate programs that may not read these variables on Windows and macOS, so there also add the authority to the system's trust store.

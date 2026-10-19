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
| `tokens` | The words that stand for the environment in resource names, such as `prd`; by default the environment's name. They are checked, but no output uses them yet |

Each resource is placed by where it was listed first, then by its tags:

- An account, subscription or project one environment names is all that environment's, whatever the resources' tags.
- One that several environments name, such as `333333333333` above, is split by their tags: a resource goes to the environment whose tags it has. One without them goes as with `--env-tags`: with the resource whose ID its import ID contains, such as a bucket's versioning, a child import with its parent, and anything else to `shared`. Each of these environments needs `tags`, with a key in common whose values differ, so that no resource can be in two; otherwise the file is rejected.
- An environment that names no account, subscription or project is found by its tags alone, in the accounts, subscriptions and projects no environment names, and those environments' tags must tell them apart too.
- An account, subscription or project no environment names, and none found by tags alone, keeps the layout without environments.

An environment's name is a directory's: lower-case letters, digits, dots, dashes and underscores, and not `shared`. `environments` doesn't go with `--env-tags`, which places resources in environments by itself, and a `--layout` of your own needs `{env}`.

A root an earlier import generated into the same `--out` keeps its directory, as its state key comes from its path: the resources the environments would now put under `envs/<env>/` stay in it, with `--regenerate` and `--incremental` too, and the report names each such root. To lay an estate out by environment from the start, import it into a new `--out`.

## Behind a TLS-intercepting proxy

If a proxy re-signs HTTPS traffic with its own certificate authority, point `SSL_CERT_FILE` at a PEM file of the certificates to trust (that authority's and the public ones), or `SSL_CERT_DIR` at a directory of them. infraharvest then trusts those certificates instead of the system's, on Windows and macOS as well as on Linux. Terraform and the providers are separate programs that may not read these variables on Windows and macOS, so there also add the authority to the system's trust store.

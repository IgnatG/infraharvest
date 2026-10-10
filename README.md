# infraharvest

infraharvest generates Terraform configuration for infrastructure that already exists (reverse Terraform). It lists your resources, writes an `import` block for each, and has Terraform (or OpenTofu) generate the configuration with `plan -generate-config-out`. It then tidies that configuration and checks that it plans with no changes.

> **Work in progress.** infraharvest started as a fork of [Terraformer](https://github.com/GoogleCloudPlatform/terraformer), which Google archived on 16 March 2026. It keeps Terraformer's listers, and is being modernised step by step.

Licensed under [AGPL-3.0](LICENSE). Terraformer code keeps its Apache-2.0 licence and attribution; see [NOTICE](NOTICE).

Coming from Terraformer? See [Migrating from Terraformer](docs/migrating-from-terraformer.md).

# Table of Contents
- [How it works](#how-it-works)
- [Installation](#installation)
- [Usage](#usage)
- [Supported Providers](/docs)
    * Major Cloud
        * [Google Cloud](/docs/gcp.md)
        * [AWS](/docs/aws.md)
        * [Azure](/docs/azure.md)
        * [AliCloud](/docs/alicloud.md)
        * [IBM Cloud](/docs/ibmcloud.md)
    * Cloud
        * [DigitalOcean](/docs/digitalocean.md)
        * [Equinix Metal](/docs/equinixmetal.md)
        * [Fastly](/docs/fastly.md)
        * [Heroku](/docs/heroku.md)
        * [LaunchDarkly](/docs/launchdarkly.md)
        * [Linode](/docs/linode.md)
        * [NS1](/docs/ns1.md)
        * [OpenStack](/docs/openstack.md)
        * [TencentCloud](/docs/tencentcloud.md)
        * [Vultr](/docs/vultr.md)
        * [Yandex Cloud](/docs/yandex.md)
        * [Ionos Cloud](/docs/ionoscloud.md)
    * Infrastructure Software
        * [Kubernetes](/docs/kubernetes.md)
        * [OctopusDeploy](/docs/octopus.md)
        * [RabbitMQ](/docs/rabbitmq.md)
    * Network
        * [Cloudflare](/docs/cloudflare.md)
        * [Myrasec](/docs/myrasec.md)
        * [PAN-OS](/docs/panos.md)
    * VCS
        * [Azure DevOps](/docs/azuredevops.md)
        * [GitHub](/docs/github.md)
        * [Gitlab](/docs/gitlab.md)
    * Monitoring & System Management
        * [Datadog](/docs/datadog.md)
        * [New Relic](/docs/relic.md)
        * [Mackerel](/docs/mackerel.md)
        * [PagerDuty](/docs/pagerduty.md)
        * [Opsgenie](/docs/opsgenie.md)
        * [Honeycomb.io](/docs/honeycombio.md)
        * [Opal](/docs/opal.md)
    * Community
        * [Keycloak](/docs/keycloak.md)
        * [Logz.io](/docs/logz.md)
        * [Commercetools](/docs/commercetools.md)
        * [Mikrotik](/docs/mikrotik.md)
        * [Xen Orchestra](/docs/xen.md)
        * [GmailFilter](/docs/gmailfilter.md)
        * [Grafana](/docs/grafana.md)
        * [Vault](/docs/vault.md)
    * Identity
        * [Okta](/docs/okta.md)
        * [Auth0](/docs/auth0.md)
        * [AzureAD](/docs/azuread.md)
- [Adding a provider](#adding-a-provider)
- [Contributing](#contributing)

## How it works

1.  The provider's listers call the cloud's APIs and record each resource's type and the ID Terraform imports it by.
2.  The selection decides which of them to import (see [Choosing what to import](#choosing-what-to-import)).
3.  infraharvest writes an `import` block for each one into a root, and runs `terraform plan -generate-config-out` (or `tofu`), so the provider itself reads every resource and writes its configuration.
4.  It tidies the generated configuration: references between resources, shared tags and identifiers as locals, secrets as variables, and clusters of resources as module calls. Each change is kept only if a new plan shows no extra changes.
5.  It runs the verification gate on every root and writes a report.

infraharvest reads the cloud and writes files; it never applies anything, and writes no state. `terraform apply` on a generated root records the imported resources in state without changing them.

## Installation

infraharvest needs Terraform or OpenTofu to generate configuration. With the default `--engine=terraform`, it uses the Terraform on `PATH` if it is 1.5 or later, or else downloads the latest release and verifies it. `--engine=tofu` needs OpenTofu 1.6 or later installed. `--terraform-path` names a binary to use instead. Terraform downloads the providers itself, so there is nothing else to install.

**From a release**

Each [release](https://github.com/IgnatG/infraharvest/releases) has archives for Linux, macOS and Windows on amd64 and arm64. It also has a checksum file, signed with [cosign](https://github.com/sigstore/cosign) from the release workflow (keyless, no long-lived key), SBOMs and build provenance. To verify a download, check the checksum file's signature, then the archive's checksum:

```sh
VERSION=0.2.0
cosign verify-blob \
  --certificate-identity "https://github.com/IgnatG/infraharvest/.github/workflows/release.yaml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle "infraharvest_${VERSION}_SHA256SUMS.sigstore.json" "infraharvest_${VERSION}_SHA256SUMS"
sha256sum --check --ignore-missing "infraharvest_${VERSION}_SHA256SUMS"
```

**Container image**

`ghcr.io/ignatg/infraharvest` (linux/amd64 and linux/arm64, signed with cosign) includes pinned Terraform and OpenTofu binaries, and git for registry modules. It runs as a non-root user in `/work`:

```sh
docker run --rm -v "$PWD:/work" -v "$HOME/.aws:/home/git/.aws:ro" -e AWS_PROFILE \
  ghcr.io/ignatg/infraharvest import aws --all --resources=vpc --regions=eu-west-2
```

**With the install script**

For Linux and macOS, including AWS CloudShell, Azure Cloud Shell and Google Cloud Shell. It checks the archive's checksum and the release's signature, so it needs [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) installed. Set `INFRAHARVEST_SKIP_SIGNATURE=1` to install with the checksum check only:

```sh
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
```

**With Go**

`go install github.com/IgnatG/infraharvest@latest`

**From source**
1.  Run `git clone https://github.com/IgnatG/infraharvest.git && cd infraharvest/`
2.  Run `go mod download`
3.  Run `go build -o infraharvest .` for all providers, or build only the providers you need:
`go build -tags minimal,aws,google -o infraharvest .` (provider names as in `infraharvest import <provider>`)

## Usage

```
infraharvest discover <provider> [flags]     list resources into a selection file to review
infraharvest pick --selection <file>         review a selection file in the terminal
infraharvest import <provider> [flags]       generate configuration for what a selection includes
infraharvest import <provider> list          list the provider's services
infraharvest verify [output-directory]       run the verification gate on generated roots again
infraharvest report [output-directory]       print an import's report again
infraharvest bootstrap --config <file>       write a root that creates the state storage
infraharvest mcp                             serve infraharvest to AI agents over MCP
infraharvest version

Flags of import and discover:
  -r, --resources strings         services to import, such as vpc,subnet,sg, or "*" for all
  -x, --excludes strings          services to leave out of --resources
  -f, --filter strings            keeps only resources with these IDs, or attributes some listers support (see Filtering)
      --selection string          selection file from infraharvest discover (discover: the file to write,
                                  default selection.yaml)
      --all                       import everything the default selection includes, without a selection file
      --engine string             terraform or tofu (default "terraform")
      --terraform-path string     Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5, else the
                                  latest release, downloaded and verified; OpenTofu >= 1.6, which must be installed)
  -o, --path-output string        output directory (default "generated")
  -p, --path-pattern string       layout of the roots (default "{output}/{provider}/{account}/{region}/")
      --config string             configuration file, which sets flags not given on the command line and the
                                  state backend of the generated roots
      --managed-state strings     leave out what Terraform already manages, according to this state
      --resume                    keep the roots a previous run generated from the same resources and options
      --incremental               add what is new to the roots earlier imports generated
      --reuse-inventory           import from the resources discover listed, instead of listing them again
      --report-tags strings       tag keys, such as owner,team, to count the report by
      --modules string            registry, latest-untested, local or none (default "registry")
      --allow-partial             leave out what fails to import, and exit 3 instead of 1
      --list-timeout duration     longest time to list one service in one region; a service that takes
                                  longer is reported as failed, 0 for no limit (default 30m0s)
  -O, --output string             hcl, or json to also print the import report as JSON on stdout (default "hcl")
  -v, --verbose                   verbose mode
```

Providers add their own flags, such as `--profile` and `--regions` for AWS or `--projects` for Google Cloud: see each provider's page under [docs](/docs).

### Choosing what to import

An import must say what it imports, so a whole account never comes under Terraform by accident:

```
infraharvest discover aws --resources=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
# review selection.yaml: set include to false to leave a resource out
infraharvest import aws --resources=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
```

`discover` lists every resource it finds into the selection file, each marked included or not. By default it leaves out resources AWS creates and manages itself, with the reason: the default VPC with its subnets, route tables and internet gateway, default security groups and network ACLs, service-linked roles, and the log groups Lambda creates. It also leaves out what CloudFormation stacks manage, including CDK apps, with the stack named as the reason: importing those would give them two owners. Global services such as IAM are checked against the stacks of every enabled region. You can include any of them by setting `include: true`. Rules in the file (`exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }`) decide resources it doesn't list, such as ones created since. Each entry records the resource's tags (labels on Google Cloud) where discover can read them, and rules can match tags too: `exclude: { tags: { env: dev } }` leaves out what is tagged `env=dev`, and `{ owner: "" }` matches resources without an `owner` tag. A bucket's configuration resources (versioning, encryption, ...) follow the bucket.

In a terminal, `discover` then opens the picker on the file (`--pick=false` skips it); `infraharvest pick --selection selection.yaml` opens it again later. The picker shows the resources as a tree, by account and region, then type. You can include or exclude one resource, or a whole type or account at once, and search by type, ID, name, note, reason or tag. `team=web` in the search shows only resources tagged `team` with a value containing `web`, and `team=` those without a `team` tag; several terms must all match. You can also show only what is new since the last `discover`. A summary keeps count of what is selected, how many roots it makes, and how many resources may become calls of curated modules. `s` saves the file and `q` leaves it as it was. Deciding on a resource marked `new` removes the mark. `discover` records each resource's account and region in the file (`scope: aws/123456789012/eu-west-2`).

`--all` imports everything the default selection includes, without a file. The report lists what was excluded and why.

Running `discover` again updates the selection file. Entries keep their decisions and notes, resources that no longer exist are dropped, and new ones are added with `new: true`, decided by the file's rules and defaults. Review those, then remove the mark.

`discover` also saves what it listed under `<path-output>/.infraharvest`. `import --selection selection.yaml --reuse-inventory` imports from that saved list instead of listing the cloud again, as long as it covers the same services, region and account. On a large estate, that saves the listing time a second time. Child resources, such as an S3 bucket's configuration, are still read at import.

Resources that Terraform already manages can be left out too. `--managed-state` reads existing state, including the version 3 state that Terraformer and earlier infraharvest releases wrote, and excludes every resource it finds there, with the state file as the reason. The flag accepts state files, directories of them, `s3://bucket/prefix?region=...`, `gs://bucket/prefix`, or `https://<account>.blob.core.windows.net/<container>/prefix`, reading every `.tfstate` object under the prefix with the cloud's default credentials. S3 state is read with `--profile`, unless the source names another with `&profile=...`. Blob Storage is read as the azurerm backend reads it: with `ARM_ACCESS_KEY`, `ARM_SAS_TOKEN` or a service principal's `ARM_CLIENT_*` variables, and otherwise Azure's default credentials, such as the Azure CLI login, which need a data role such as Storage Blob Data Reader. `tfc://<organization>/<workspace>` reads the current state of an HCP Terraform workspace, and `tfc://<organization>/<prefix>*` of every workspace whose name starts with the prefix (`?host=` names a Terraform Enterprise host), with the token `terraform login` saved or `TF_TOKEN_<host>`. `--managed-state=backend` reads the S3, GCS or azurerm backend from the configuration file, so an import run again only picks up what is new. The report then shows how much of what was discovered Terraform manages, how much another tool such as CloudFormation manages, and how much neither does. It breaks this down by type, and by account and region when the import covers more than one (`scopes` in `coverage.json`). State can hold secrets: infraharvest reads it only when asked, keeps only resource types and IDs, and needs read access to the state for it.

A run that fails part way, such as on one region, can be run again with `--resume`: roots generated from the same resources and options since then are kept as they are, with their results. The others are generated again from scratch. Checkpoints go into `<path-output>/.infraharvest`, which `.gitignore` excludes.

Once the roots are in use, `--incremental` adds what is new to them instead, without changing what they have. Each new resource goes into a file of its own, `generated_2.tf`, then `generated_3.tf` and so on. Its import blocks, variables, locals and data sources are added after the root's own, and nothing new takes a name the root uses. New resources refer to the resources the root has, such as `vpc_id = aws_vpc.main.id`. They don't repeat the tags the root's provider already applies. A root's resources are found from its import blocks and from the checkpoint of the import that generated it; for roots applied since, with their import blocks deleted, add `--managed-state` too. The new resources are generated and planned in a directory of their own first, because planning the root itself would need its state. In the root, the other checks run on the files as merged. Clusters of new resources can become curated module calls, but they aren't moved into generated local modules.

```sh
infraharvest discover aws --regions=eu-west-2 --selection=selection.yaml   # marks what is new
infraharvest import aws --regions=eu-west-2 --selection=selection.yaml --incremental --managed-state=backend
```

### Configuration file and state backend

`--config infraharvest.yaml` sets any flag the command line doesn't, and the state backend of the generated roots:

```yaml
version: 1
settings:                # flags of every provider command
  selection: selection.yaml
providers:               # flags of one provider command
  aws:
    profile: prod
    regions: [eu-west-2, us-east-1]
    resources: [vpc, subnet, sg, s3]
backend:                 # one of s3, azurerm, gcs
  s3:
    bucket: acme-terraform-state
    region: eu-west-2
    key_prefix: imported
```

Each root gets a `backend.tf` with its own state key (`imported/aws/<account>/<region>/terraform.tfstate`). S3 state is locked with S3's own lock file (`use_lockfile = true`), not DynamoDB. infraharvest writes `backend.tf` after checking the root, so importing never needs access to the state bucket.

If the storage doesn't exist yet, `infraharvest bootstrap --config infraharvest.yaml` writes a root into `<path-output>/bootstrap` that creates it: an S3 bucket, an Azure storage account and container, or a Cloud Storage bucket. The storage is versioned, encrypted, not public, reachable only over TLS, and protected from `terraform destroy`. Apply it once, with rights to create storage, before planning the generated roots.

### AI agents (MCP)

`infraharvest mcp` serves infraharvest to AI agents such as Claude Code, Copilot or Cursor over the [Model Context Protocol](https://modelcontextprotocol.io), on stdin and stdout. Its tools run the same binary, so they behave like the command line:

| Tool | What it does |
|---|---|
| `discover` | Lists a provider's resources into a selection file, and summarises what it includes and excludes, with reasons |
| `import` | Generates configuration from a selection file. It asks the user to confirm first, through MCP elicitation. A client that can't ask gets the command to run instead |
| `report` | Returns an import's report |

The agent can propose a selection, but only a person can start an import. infraharvest reads the cloud and writes files; it never applies anything. To add the server to Claude Code:

```sh
claude mcp add infraharvest -- infraharvest mcp
```

### Output

By default each root is one state boundary: `<path-output>/<provider>/<account>/<region>/`, with `global` for global services such as IAM. Only AWS reports its account and region so far; for other providers both are `default`. `--path-pattern` can change that, with `{account}` and `{region}` as well as `{output}`, `{provider}` and `{service}`.

Each output directory gets:

| File | Contents |
|---|---|
| `versions.tf` | `required_version` within the major release of the Terraform that generated it (`>= 1.16, < 2.0`), and the provider pinned to its newest minor release line (`~> 6.67`) |
| `providers.tf` | The provider configuration |
| `imports.tf` | One `import` block per resource. Delete it after the first `terraform apply` |
| `generated.tf` | The configuration Terraform generated for the resources |
| `variables.tf` | Only if there are secrets: a `sensitive` variable, with no default, for each secret value. Terraform doesn't write secret values (an SSM parameter's `value`, for example) into the configuration it generates. Set the variables before planning, for example in a `.tfvars` file kept out of version control. Write-only arguments (`*_wo`) stay unset |
| `.terraform.lock.hcl` | The provider version, the same for every directory of one import |
| `rejected.hcl` | Only if some resources couldn't be imported: their `import` block and generated configuration, under the errors Terraform reported. Terraform doesn't load this file. Fix a resource and move its blocks into `imports.tf` and `generated.tf`, or leave it out. Rejected resources make the import exit non-zero unless `--allow-partial` is set |

Each directory also gets a `README.md` with what was imported and the steps left to take, and the output directory gets a `.gitignore` for state, plans, `.terraform/` and `.tfvars` files, unless it already has one. Resource names are snake_case labels made from the names the resources have in the cloud.

Terraform writes every optional argument into the configuration it generates, so infraharvest leaves out the ones that only repeat a default. That means arguments set to `null`, and optional arguments set to `false`, `0`, `""`, `[]` or `{}` when that is the provider's default. Only the plan can tell a default from a setting, so infraharvest plans without these arguments and puts back any whose absence would change a resource. Computed arguments keep their values: leaving one out never shows in the plan, so it could hide a real setting.

Literals that are another imported resource's ID or ARN become references: `vpc_id = aws_vpc.main.id`, `role_arn = aws_iam_role.app.arn`. An argument named after a resource type refers to that resource by name, for example `bucket = aws_s3_bucket.logs.bucket`. Values several resources share are skipped, unless one is the others' parent (a bucket and its configuration resources). So are references that would make resources depend on each other in a loop. The references are kept only if a new plan shows no extra changes.

A resource the import listed but the selection left out, such as a default VPC or a default security group, is read through a data source instead: `security_groups = [aws_security_group.web.id, data.aws_security_group.sg_0abc1234.id]`, with the `data` blocks in `data.tf`. This only happens when the resource type has a data source that reads it by its ID, and the result is checked by plan in the same way.

Repeated values move into `locals.tf`:

- **Shared tags:** tags every resource in a directory shares become `local.tags`. On AWS they are applied through the provider's `default_tags`, and each resource keeps only its other tags. Because AWS records every tag in `tags_all`, the move is kept only if a new plan shows no extra changes. On Google Cloud, labels every resource shares become `local.labels`, applied through the provider's `default_labels`; `goog-` labels stay on the resources.
- **Repeated identifiers:** IDs and ARNs used three or more times (`vpc_id = "vpc-0abc1234"`) become locals named after the argument that uses them (`local.vpc_id`).

AWS resources don't repeat `region` (the provider's) or the computed `tags_all`.

Clusters of resources that a curated public module can manage move into a call of that module, at an exact version. For each cluster, infraharvest maps the generated configuration onto the module's inputs. It declines a cluster when the module can't express one of its settings, rather than drop the setting. It then plans, and takes back any call whose resources would plan differently than they did in the root, or for which the module would create anything else. The root README and the report list each call, and each cluster that stayed in the root with the reason. These modules are mapped so far:

| Resources | Module |
|---|---|
| `aws_s3_bucket` with its versioning, encryption, public access block, ownership controls, lifecycle and policy | [`terraform-aws-modules/s3-bucket/aws`](https://registry.terraform.io/modules/terraform-aws-modules/s3-bucket/aws) 5.16.1 |
| `aws_iam_role` with its policy attachments, and an inline policy and instance profile named after it | [`terraform-aws-modules/iam/aws//modules/iam-role`](https://registry.terraform.io/modules/terraform-aws-modules/iam/aws/latest/submodules/iam-role) 6.8.2 |

A module may set arguments the provider keeps only in state, such as the iam-role module's `force_detach_policies`. Import can't set those. The plan then updates them in state alone, and the verification gate lists those updates.

Each adapter pins an exact module version and is checked against that version's variables and outputs. At import, the registry says whether a module has a newer release: the report names it, and `--modules latest-untested` calls the newest release instead of the tested one (the plan check still decides whether each call is kept). The provider is pinned to its newest release that the module versions called accept; when one of them holds it back, the report says which. A nightly job opens an issue when a newer release comes out, saying whether the adapter fits it; `go run ./adapters/cmd/adaptercheck -write` refreshes the interfaces after a bump.

`--modules=local` uses generated local modules only, for example where the module registry can't be reached; `--modules=none` keeps every resource in the root.

Other resources that come in clusters, such as a security group and its rules, move into a generated local module when two or more clusters in a root have the same shape. The module goes in `<path-output>/modules/<kind>_<hash>/` (`main.tf`, `variables.tf`, `outputs.tf`, `README.md`), and each cluster becomes a call to it. Values the clusters share stay in the module, and values that differ become typed variables. References from other resources use the module's outputs, and the import blocks import into the module. Identical modules in different roots are written once. The change is kept only if a new plan shows no extra changes.

S3 buckets are imported split, as the AWS provider recommends: each part of a bucket's configuration it has (versioning, encryption, lifecycle, CORS, website, logging, public access block, ownership controls, transfer acceleration, requester pays, object lock, replication, policy) is its own resource, and the bucket's deprecated inline arguments are left out. ACLs aren't imported yet.

The `report/` directory of the output records the import:

| File | Contents |
|---|---|
| `coverage.json` | What the listers found, by type and directory, and what became of it: imported, left out (with Terraform's errors), not importable, or lost to a failed directory or service. Also the secret variables to set |
| `manifest.json` | The versions used: infraharvest, Terraform or OpenTofu, and the provider (constraint and locked version) |
| `report.md` | The same, for people |

With `--report-tags owner,team`, the report also counts what was discovered, imported and managed by the value of each tag key, including resources without the tag, so you can see which owners still have resources outside infrastructure as code. It counts the resources whose tags discover could read (see Choosing what to import). Reports hold no timestamps or absolute paths, so importing an unchanged estate produces the same files. With `--output json`, the whole report is also printed to stdout as one JSON document (`schema_version` 1); logs always go to stderr.

After generating a directory, infraharvest runs its verification gate on it and records the results in the directory's README and in `coverage.json`:

| Check | Passes when |
|---|---|
| G1 format | `terraform fmt` has nothing to change |
| G2 validate | `terraform validate` passes |
| G3 plan | The plan imports every resource and changes nothing else. Secret variables get placeholder values for this plan, so the arguments they set may change, and so may arguments the provider keeps only in state (for example a Secrets Manager secret's `recovery_window_in_days`) |
| G4 standards | The directory, and the local modules it calls, follow the output standard. Terraform and providers are pinned (`required_version` within one major release, providers with `~>`). Registry modules have an exact version, and git modules a `?ref=`. Variables and outputs are typed and described, and no sensitive variable has a default. There is no `"${...}"` around a single expression, and no argument the provider leaves out (such as an S3 bucket's deprecated inline settings). There are no state files and only `.tf` files. Local modules have `main.tf`, `variables.tf`, `outputs.tf`, `versions.tf` and a README, and no `examples/` |
| G5 scanners | The scanners installed on `PATH` run on the directory. tflint must find nothing at warning level or above, since its findings concern the generated code. trivy and checkov findings are listed but never fail the check: they describe the infrastructure's own settings, which the configuration must mirror to plan with no changes |
| G6 secrets | No written file contains a value the provider marks sensitive, or a credential such as an AWS access key or a private key |
| G7 determinism | The configuration calls no function whose result changes between runs, such as `timestamp()` |

A failed check counts like a resource that couldn't be imported (exit code 1, or 3 with `--allow-partial`).

To check generated roots again, for example after editing them, run `infraharvest verify [output-directory]`. It runs the same checks on every root, initialising each without its backend, so it needs no access to the state; set secret variables first. `infraharvest report [output-directory]` prints an import's report again (`--output json` for JSON).

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Everything found was imported (types Terraform can't import aside) |
| 1 | Something couldn't be imported, and `--allow-partial` isn't set |
| 2 | The import couldn't run, for example without credentials or a Terraform binary |
| 3 | Something couldn't be imported, `--allow-partial` is set, and the output has the rest |

When the configuration Terraform generates doesn't validate, infraharvest fixes what Terraform rejects where that doesn't change its meaning, then plans again. It removes arguments that are unset in effect (zero values), arguments that duplicate another one (`subnets` next to `subnet_mapping` blocks), and nested blocks whose arguments are all null. Inside objects, it writes `null` for unset strings that Terraform generated as `""`.

### Permissions

infraharvest needs read-only permissions: it lists resources and lets Terraform read their configuration. [permissions](permissions/README.md) has configurations that grant the minimum access for AWS, Azure and Google Cloud.

### Resources

You can use `--resources` parameter to tell resources from what service you want to import.

To import resources from all services, use `--resources="*"` . If you want to exclude certain services, you can combine the parameter with `--excludes` to exclude resources from services you don't want to import e.g. `--resources="*" --excludes="iam"`.

### Filtering

`--filter` keeps only the resources with the IDs you name, before anything is imported. Separate several IDs with `:`, and wrap an ID that contains `:` in `'`, as in `--filter=resource=id1:'project:dataset_id'`. IDs follow each resource type's import ID, which the import section of its [Terraform provider documentation][terraform-providers] describes.

[terraform-providers]: https://registry.terraform.io/browse/providers

```
infraharvest import aws --all --resources=vpc,subnet --filter=vpc=myvpcid --regions=eu-west-1
```

This imports only the VPC `myvpcid`, and the subnets. Use `Type` when one filter should apply to one service only, and combine several filters:

```
infraharvest import aws --all --resources=sg,vpc --filter="Type=vpc;Name=id;Value=VPC_ID" --filter="Type=sg;Name=id;Value=SG_ID1:SG_ID2"
```

A filter on another attribute, such as `--resources=ec2_instance --filter="Name=tags.Team;Value=web"`, only applies where the service's lister passes it to the API it lists with: AWS EC2 instance tags, Datadog tags, and some Tencent Cloud, IBM, NS1 and Heroku services (see each provider's page). Other services ignore it and list everything, and infraharvest logs a warning. To choose resources by anything else, run `infraharvest discover`, edit the selection file it writes, and import with `--selection`.

## Adding a provider

1.  Create `providers/<name>/` with a provider and a service generator for each service. The provider implements `terraformutils.ProviderGenerator` and lists its services in `GetSupportedService`. Each service's `InitResources` lists the resources and records each one with `terraformutils.NewResource` or `NewSimpleResource`: its ID, a name, the Terraform resource type and attributes for filters.
2.  Create `cmd/provider_cmd_<name>.go` with the provider's command and flags. It registers itself with `registerProvider` from `init()`, under the build constraint `!minimal || <name>`.
3.  Add the optional interfaces in `terraformutils/base_provider.go` that the provider needs, for example `GetSource` when the provider isn't `hashicorp/<name>` on the registry, or `ImportID` when the ID a lister records isn't the one Terraform imports by.
4.  Add a page under `docs/` and link it from this README.

### GCP compute resources

For GCP compute resources, use generated code from
`providers/gcp/gcp_compute_code_generator`.

To regenerate code:

```
go run providers/gcp/gcp_compute_code_generator/*.go
```

## Contributing

If you have improvements or fixes, we would love to have your contributions.
Please read [CONTRIBUTING.md](./CONTRIBUTING.md) for more information on the process we would like
contributors to follow.

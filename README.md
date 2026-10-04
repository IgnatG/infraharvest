# infraharvest

> **Work in progress.** infraharvest is being built on top of [Terraformer](https://github.com/GoogleCloudPlatform/terraformer), which Google archived on 16 March 2026. The code is still Terraformer's and works as documented below, and is being modernised step by step.

Licensed under [AGPL-3.0](LICENSE). Terraformer code keeps its Apache-2.0 licence and attribution; see [NOTICE](NOTICE).

The rest of this README is Terraformer's documentation.

A CLI tool that generates `tf`/`json` and `tfstate` files based on existing infrastructure
(reverse Terraform).

*   Disclaimer: This is not an official Google product
*   Created by: Waze SRE

![Waze SRE logo](assets/waze-sre-logo.png)

# Table of Contents
- [Demo GCP](#demo-gcp)
- [Capabilities](#capabilities)
- [Installation](#installation)
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
        * [Cloudflare](/docs/cloudflare.md) (provider 3.x only, see the docs page)
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
- [Contributing](#contributing)
- [Developing](#developing)
- [Infrastructure](#infrastructure)
- [Stargazers over time](#stargazers-over-time)

## Demo GCP
[![asciicast](https://asciinema.org/a/243961.svg)](https://asciinema.org/a/243961)

## Capabilities

1.  Generate `tf`/`json` + `tfstate` files from existing infrastructure for all
    supported objects by resource.
2.  Remote state can be uploaded to a GCS bucket.
3.  Connect between resources with `terraform_remote_state` (local and bucket).
4.  Save `tf`/`json` files using a custom folder tree pattern.
5.  Import by resource name and type.
6.  Support terraform 0.13 (for terraform 0.11 use v0.7.9).

Terraformer uses Terraform providers and is designed to easily support newly added resources.
To upgrade resources with new fields, all you need to do is upgrade the relevant Terraform providers.
```
Import current state to Terraform configuration from a provider

Usage:
   import [provider] [flags]
   import [provider] [command]

Available Commands:
  list        List supported resources for a provider

Flags:
  -b, --bucket string         gs://terraform-state
  -c, --connect                (default true)
  -С, --compact                (default false)
  -x, --excludes strings      firewalls,networks
  -f, --filter strings        compute_firewall=id1:id2:id4
  -h, --help                  help for google
  -O, --output string         output format hcl or json (default "hcl")
  -o, --path-output string     (default "generated")
  -p, --path-pattern string   {output}/{provider}/ (default "{output}/{provider}/{service}/")
      --projects strings
  -z, --regions strings       europe-west1, (default [global])
  -r, --resources strings     firewall,networks or * for all services
  -s, --state string          local or bucket (default "local")
  -v, --verbose               verbose mode
  -n, --retry-number          number of retries to perform if refresh fails
  -m, --retry-sleep-ms        time in ms to sleep between retries
      --allow-partial         exit 0 when some services or resources fail to import (default false)
      --engine string         legacy, terraform or tofu: generate configuration with Terraform or
                              OpenTofu from import blocks; no state is written (default "legacy")
      --terraform-path string Terraform or OpenTofu binary for --engine=terraform or tofu
                              (default: on PATH; Terraform >= 1.5, else the latest release,
                              downloaded and verified; OpenTofu >= 1.6, which must be installed)

Use " import [provider] [command] --help" for more information about a command.
```
#### Choosing what to import

With `--engine=terraform` or `tofu`, an import must say what it imports, so a whole account never comes under Terraform by accident:

```
infraharvest discover aws --resources=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
# review selection.yaml: set include to false to leave a resource out
infraharvest import aws --engine=terraform --resources=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
```

`discover` lists every resource it finds into the selection file, each marked included or not. By default it leaves out resources AWS creates and manages itself, with the reason: the default VPC with its subnets, route tables and internet gateway, default security groups and network ACLs, service-linked roles, and the log groups Lambda creates. You can include any of them by setting `include: true`. Rules in the file (`exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }`) decide resources it doesn't list, such as ones created since. A bucket's configuration resources (versioning, encryption, ...) follow the bucket.

`--all` imports everything the default selection includes, without a file. The report lists what was excluded and why.

#### Configuration file and state backend

`--config infraharvest.yaml` sets any flag the command line doesn't, and the state backend of the generated roots:

```yaml
version: 1
settings:                # flags of every provider command
  engine: terraform
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

#### AI agents (MCP)

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

#### Output of `--engine=terraform`

By default each root is one state boundary: `<path-output>/<provider>/<account>/<region>/`, with `global` for global services such as IAM. `--path-pattern` can change that, with `{account}` and `{region}` as well as `{output}`, `{provider}` and `{service}`.

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

Literals that are another imported resource's ID or ARN become references: `vpc_id = aws_vpc.main.id`, `role_arn = aws_iam_role.app.arn`. An argument named after a resource type refers to that resource by name, for example `bucket = aws_s3_bucket.logs.bucket`. Values several resources share are skipped, unless one is the others' parent (a bucket and its configuration resources). So are references that would make resources depend on each other in a loop. The references are kept only if a new plan shows no extra changes.

Repeated values move into `locals.tf`:

- **Shared tags:** tags every resource in a directory shares become `local.tags`. On AWS they are applied through the provider's `default_tags`, and each resource keeps only its other tags. Because AWS records every tag in `tags_all`, the move is kept only if a new plan shows no extra changes.
- **Repeated identifiers:** IDs and ARNs used three or more times (`vpc_id = "vpc-0abc1234"`) become locals named after the argument that uses them (`local.vpc_id`).

AWS resources don't repeat `region` (the provider's) or the computed `tags_all`.

Clusters of resources that a curated public module can manage move into a call of that module, at an exact version. For each cluster, infraharvest maps the generated configuration onto the module's inputs. It declines a cluster when the module can't express one of its settings, rather than drop the setting. It then plans, and takes back any call whose resources would plan differently than they did in the root, or for which the module would create anything else. The root README and the report list each call, and each cluster that stayed in the root with the reason. These modules are mapped so far:

| Resources | Module |
|---|---|
| `aws_s3_bucket` with its versioning, encryption, public access block, ownership controls, lifecycle and policy | [`terraform-aws-modules/s3-bucket/aws`](https://registry.terraform.io/modules/terraform-aws-modules/s3-bucket/aws) 5.16.1 |

`--modules=local` uses generated local modules only, for example where the module registry can't be reached; `--modules=none` keeps every resource in the root.

Other resources that come in clusters, such as a security group and its rules, move into a generated local module when two or more clusters in a root have the same shape. The module goes in `<path-output>/modules/<kind>_<hash>/` (`main.tf`, `variables.tf`, `outputs.tf`, `README.md`), and each cluster becomes a call to it. Values the clusters share stay in the module, and values that differ become typed variables. References from other resources use the module's outputs, and the import blocks import into the module. Identical modules in different roots are written once. The change is kept only if a new plan shows no extra changes.

S3 buckets are imported split, as the AWS provider recommends: each part of a bucket's configuration it has (versioning, encryption, lifecycle, CORS, website, logging, public access block, ownership controls, transfer acceleration, requester pays, object lock, replication, policy) is its own resource, and the bucket's deprecated inline arguments are left out. ACLs aren't imported yet.

The `report/` directory of the output records the import:

| File | Contents |
|---|---|
| `coverage.json` | What the listers found, by type and directory, and what became of it: imported, left out (with Terraform's errors), not importable, or lost to a failed directory or service. Also the secret variables to set |
| `manifest.json` | The versions used: infraharvest, Terraform or OpenTofu, and the provider (constraint and locked version) |
| `report.md` | The same, for people |

Reports hold no timestamps or absolute paths, so importing an unchanged estate produces the same files. With `--output json`, the whole report is also printed to stdout as one JSON document (`schema_version` 1); logs always go to stderr.

After generating a directory, infraharvest runs its verification gate on it and records the results in the directory's README and in `coverage.json`:

| Check | Passes when |
|---|---|
| G1 format | `terraform fmt` has nothing to change |
| G2 validate | `terraform validate` passes |
| G3 plan | The plan imports every resource and changes nothing else. Secret variables get placeholder values for this plan, so the arguments they set may change, and so may arguments the provider keeps only in state (for example a Secrets Manager secret's `recovery_window_in_days`) |
| G6 secrets | No written file contains a value the provider marks sensitive, or a credential such as an AWS access key or a private key |
| G7 determinism | The configuration calls no function whose result changes between runs, such as `timestamp()` |

A failed check counts like a resource that couldn't be imported (exit code 1, or 3 with `--allow-partial`).

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Everything found was imported (types Terraform can't import aside) |
| 1 | Something couldn't be imported, and `--allow-partial` isn't set |
| 2 | The import couldn't run, for example without credentials or a Terraform binary |
| 3 | Something couldn't be imported, `--allow-partial` is set, and the output has the rest |

When the configuration Terraform generates doesn't validate, infraharvest fixes what Terraform rejects where that doesn't change its meaning, then plans again. It removes arguments that are unset in effect (zero values), arguments that duplicate another one (`subnets` next to `subnet_mapping` blocks), and nested blocks whose arguments are all null. Inside objects, it writes `null` for unset strings that Terraform generated as `""`.

#### Permissions

The tool requires read-only permissions to list service resources.

#### Resources

You can use `--resources` parameter to tell resources from what service you want to import.

To import resources from all services, use `--resources="*"` . If you want to exclude certain services, you can combine the parameter with `--excludes` to exclude resources from services you don't want to import e.g. `--resources="*" --excludes="iam"`.

#### Filtering

Filters are a way to choose which resources `terraformer` imports. It's possible to filter resources by its identifiers or attributes. Multiple filtering values are separated by `:`. If an identifier contains this symbol, value should be wrapped in `'` e.g. `--filter=resource=id1:'project:dataset_id'`. Identifier based filters will be executed before Terraformer will try to refresh remote state.

Use `Type` when you need to filter only one of several types of resources. Multiple filters can be combined when importing different resource types. An example would be importing all AWS security groups from a specific AWS VPC:
```
infraharvest import aws -r sg,vpc --filter Type=sg;Name=vpc_id;Value=VPC_ID --filter Type=vpc;Name=id;Value=VPC_ID
```
Notice how the `Name` is different for `sg` than it is for `vpc`.

##### Migration state version
For terraform >= 0.13, you can use `replace-provider` to migrate state from previous versions.

Example usage:
```
terraform state replace-provider -auto-approve "registry.terraform.io/-/aws" "hashicorp/aws"
```

##### Resource ID

Filtering is based on Terraform resource ID patterns. To find valid ID patterns for your resource, check the import part of the [Terraform documentation][terraform-providers].

[terraform-providers]: https://www.terraform.io/docs/providers/

Example usage:

```
infraharvest import aws --resources=vpc,subnet --filter=vpc=myvpcid --regions=eu-west-1
```
Will only import the vpc with id `myvpcid`. This form of filters can help when it's necessary to select resources by its identifiers.

##### Field name only

It is possible to filter by specific field name only. It can be used e.g. when you want to retrieve resources only with a specific tag key.

Example usage:

```
infraharvest import aws --resources=s3 --filter="Name=tags.Abc" --regions=eu-west-1
```
Will only import the s3 resources that have tag `Abc`. This form of filters can help when the field values are not important from filtering perspective.

##### Field with dots

It is possible to filter by a field that contains a dot.

Example usage:

```
infraharvest import aws --resources=s3 --filter="Name=tags.Abc.def" --regions=eu-west-1
```
Will only import the s3 resources that have tag `Abc.def`.

#### Planning

The `plan` command generates a planfile that contains all the resources set to be imported. By modifying the planfile before running the `import` command, you can rename or filter the resources you'd like to import.

The rest of subcommands and parameters are identical to the `import` command.

```
$ infraharvest plan google --resources=networks,firewall --projects=my-project --regions=europe-west1-d
(snip)

Saving planfile to generated/google/my-project/terraformer/plan.json
```

After reviewing/customizing the planfile, begin the import by running `import plan`.

```
$ infraharvest import plan generated/google/my-project/terraformer/plan.json
```

### Resource structure

Terraformer by default separates each resource into a file, which is put into a given service directory.

The default path for resource files is `{output}/{provider}/{service}/{resource}.tf` and can vary for each provider.

It's possible to adjust the generated structure by:
1. Using `--compact` parameter to group resource files within a single service into one `resources.tf` file
2. Adjusting the `--path-pattern` parameter and passing e.g. `--path-pattern {output}/{provider}/` to generate resources for all services in one directory

It's possible to combine `--compact` `--path-pattern` parameters together.

### Installation

Both Terraformer and a Terraform provider plugin need to be installed.

#### Terraformer

**From a package manager**
- [Homebrew](https://brew.sh/) users can use `brew install terraformer`.
- [MacPorts](https://www.macports.org/) users can use `sudo port install terraformer`.
- [Chocolatey](https://chocolatey.org/) users can use `choco install terraformer`.

**From releases**
This installs all providers, set `PROVIDER` to one of `google`, `aws` or `kubernetes` if you only need one.

* Linux
```
export PROVIDER=all
curl -LO "https://github.com/GoogleCloudPlatform/terraformer/releases/download/$(curl -s https://api.github.com/repos/GoogleCloudPlatform/terraformer/releases/latest | grep tag_name | cut -d '"' -f 4)/terraformer-${PROVIDER}-linux-amd64"
chmod +x terraformer-${PROVIDER}-linux-amd64
sudo mv terraformer-${PROVIDER}-linux-amd64 /usr/local/bin/terraformer
```
* MacOS
```
export PROVIDER=all
curl -LO "https://github.com/GoogleCloudPlatform/terraformer/releases/download/$(curl -s https://api.github.com/repos/GoogleCloudPlatform/terraformer/releases/latest | grep tag_name | cut -d '"' -f 4)/terraformer-${PROVIDER}-darwin-amd64"
chmod +x terraformer-${PROVIDER}-darwin-amd64
sudo mv terraformer-${PROVIDER}-darwin-amd64 /usr/local/bin/terraformer
```
* Windows
1. Install Terraform - https://www.terraform.io/downloads
2. Download exe file for required provider from here - https://github.com/GoogleCloudPlatform/terraformer/releases
3. Add the exe file path to path variable

**From source**
1.  Run `git clone https://github.com/IgnatG/infraharvest.git && cd infraharvest/`
2.  Run `go mod download`
3.  Run `go build -o infraharvest .` for all providers, or build only the providers you need:
`go build -tags minimal,aws,google -o infraharvest .` (provider names as in `infraharvest import <provider>`)

#### Terraform Providers

Create a working folder and initialize the Terraform provider plugin.  This folder will be where you run Terraformer commands.

Run ```terraform init``` against a ```versions.tf``` file to install the plugins required for your platform. For example, if you need plugins for the google provider, ```versions.tf``` should contain:
```
terraform {
  required_providers {
    google = {
      source = "hashicorp/google"
    }
  }
  required_version = ">= 0.13"
}
```

Or, copy your Terraform provider's plugin(s) from the list below to folder `~/.terraform.d/plugins/`, as appropriate.

Links to download Terraform provider plugins:
* Major Cloud
    * Google Cloud provider >2.11.0 - [here](https://releases.hashicorp.com/terraform-provider-google/)
    * AWS provider >2.25.0 - [here](https://releases.hashicorp.com/terraform-provider-aws/)
    * Azure provider >1.35.0 - [here](https://releases.hashicorp.com/terraform-provider-azurerm/)
    * Alicloud provider >1.57.1 - [here](https://releases.hashicorp.com/terraform-provider-alicloud/)
* Cloud
    * DigitalOcean provider >1.9.1 - [here](https://releases.hashicorp.com/terraform-provider-digitalocean/)
    * Heroku provider >2.2.1 - [here](https://releases.hashicorp.com/terraform-provider-heroku/)
    * LaunchDarkly provider >=2.1.1 - [here](https://releases.hashicorp.com/terraform-provider-launchdarkly/)
    * Linode provider >1.8.0 - [here](https://releases.hashicorp.com/terraform-provider-linode/)
    * OpenStack provider >1.21.1 - [here](https://releases.hashicorp.com/terraform-provider-openstack/)
    * TencentCloud provider >1.50.0 - [here](https://releases.hashicorp.com/terraform-provider-tencentcloud/)
    * Vultr provider >1.0.5 - [here](https://releases.hashicorp.com/terraform-provider-vultr/)
    * Yandex provider >0.42.0 - [here](https://releases.hashicorp.com/terraform-provider-yandex/)
    * Ionoscloud provider >6.3.3 - [here](https://github.com/ionos-cloud/terraform-provider-ionoscloud/releases)
* Infrastructure Software
    * Kubernetes provider >=1.9.0 - [here](https://releases.hashicorp.com/terraform-provider-kubernetes/)
    * RabbitMQ provider >=1.1.0 - [here](https://releases.hashicorp.com/terraform-provider-rabbitmq/)
* Network
    * Myrasec provider >1.44 - [here](https://github.com/Myra-Security-GmbH/terraform-provider-myrasec)
    * Cloudflare provider >1.16, <4.0 - [here](https://releases.hashicorp.com/terraform-provider-cloudflare/)
    * Fastly provider >0.16.1 - [here](https://releases.hashicorp.com/terraform-provider-fastly/)
    * NS1 provider >1.8.3 - [here](https://releases.hashicorp.com/terraform-provider-ns1/)
    * PAN-OS provider >= 1.8.3 - [here](https://github.com/PaloAltoNetworks/terraform-provider-panos)
* VCS
    * GitHub provider >=2.2.1 - [here](https://releases.hashicorp.com/terraform-provider-github/)
* Monitoring & System Management
    * Datadog provider >2.1.0 - [here](https://releases.hashicorp.com/terraform-provider-datadog/)
    * New Relic provider >2.0.0 - [here](https://releases.hashicorp.com/terraform-provider-newrelic/)
    * Mackerel provider > 0.0.6 - [here](https://github.com/mackerelio-labs/terraform-provider-mackerel)
    * Pagerduty >=1.9 - [here](https://releases.hashicorp.com/terraform-provider-pagerduty/)
    * Opsgenie >= 0.6.0 [here](https://releases.hashicorp.com/terraform-provider-opsgenie/)
    * Honeycomb.io >= 0.10.0 - [here](https://github.com/honeycombio/terraform-provider-honeycombio/releases)
    * Opal >= 0.0.2 - [here](https://github.com/opalsecurity/terraform-provider-opal/releases)
* Community
    * Keycloak provider >=1.19.0 - [here](https://github.com/mrparkers/terraform-provider-keycloak/)
    * Logz.io provider >=1.1.1 - [here](https://github.com/jonboydell/logzio_terraform_provider/)
    * Commercetools provider >= 0.21.0 - [here](https://github.com/labd/terraform-provider-commercetools)
    * Mikrotik provider >= 0.2.2 - [here](https://github.com/ddelnano/terraform-provider-mikrotik)
    * Xen Orchestra provider >= 0.18.0 - [here](https://github.com/ddelnano/terraform-provider-xenorchestra)
    * GmailFilter provider >= 1.0.1 - [here](https://github.com/yamamoto-febc/terraform-provider-gmailfilter)
    * Vault provider - [here](https://github.com/hashicorp/terraform-provider-vault)
    * Auth0 provider - [here](https://github.com/alexkappa/terraform-provider-auth0)
    * AzureAD provider - [here](https://github.com/hashicorp/terraform-provider-azuread)

Information on provider plugins:
https://www.terraform.io/docs/configuration/providers.html


## High-Level steps to add new provider
 * Initialize provider details in cmd/root.go and create a provider initialization file in the terraformer/cmd folder
 * Create a folder under terraformer/providers/ for your provider
 * Create two files under this folder
   * <provide_name>_provider.go
   * <provide_name>_service.go
* Initialize all provider's supported services in <provide_name>_provider.go file
* Create script for each supported service in same folder

## Contributing

If you have improvements or fixes, we would love to have your contributions.
Please read [CONTRIBUTING.md](./CONTRIBUTING.md) for more information on the process we would like
contributors to follow.

## Developing

Terraformer was built so you can easily add new providers of any kind.

Process for generating `tf`/`json` + `tfstate` files:

1.  Call GCP/AWS/other api and get list of resources.
2.  Iterate over resources and take only the ID (we don't need mapping fields!).
3.  Call to provider for readonly fields.
4.  Call to infrastructure and take tf + tfstate.

## Infrastructure

1.  Call to provider using the refresh method and get all data.
2.  Convert refresh data to go struct.
3.  Generate HCL file - `tf`/`json` files.
4.  Generate `tfstate` files.

All mapping of resource is made by providers and Terraform. Upgrades are needed only
for providers.

##### GCP compute resources

For GCP compute resources, use generated code from
`providers/gcp/gcp_compute_code_generator`.

To regenerate code:

```
go run providers/gcp/gcp_compute_code_generator/*.go
```

### Similar projects

#### [terraforming](https://github.com/dtan4/terraforming)

##### Terraformer Benefits

* Simpler to add new providers and resources - already supports AWS, GCP, GitHub, Kubernetes, and Openstack. Terraforming supports only AWS.
* Better support for HCL + tfstate, including updates for Terraform 0.12.
* If a provider adds new attributes to a resource, there is no need change Terraformer code - just update the Terraform provider on your laptop.
* Automatically supports connections between resources in HCL files.

##### Comparison

Terraforming gets all attributes from cloud APIs and creates HCL and tfstate files with templating. Each attribute in the API needs to map to attribute in Terraform. Generated files from templating can be broken with illegal syntax. When a provider adds new attributes the terraforming code needs to be updated.

Terraformer instead uses Terraform provider files for mapping attributes, HCL library from Hashicorp, and Terraform code.

Look for S3 support in terraforming here and official S3 support
Terraforming lacks full coverage for resources - as an example you can see that 70% of S3 options are not supported:

* terraforming - https://github.com/dtan4/terraforming/blob/master/lib/terraforming/template/tf/s3.erb
* official S3 support - https://www.terraform.io/docs/providers/aws/r/s3_bucket

## Stargazers over time

[![Stargazers over time](https://starchart.cc/GoogleCloudPlatform/terraformer.svg)](https://starchart.cc/GoogleCloudPlatform/terraformer)

# infraharvest

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/IgnatG/infraharvest/badge)](https://scorecard.dev/viewer/?uri=github.com/IgnatG/infraharvest)

infraharvest generates Terraform configuration for infrastructure that already exists. It lists your resources, writes an `import` block for each, and has Terraform (or OpenTofu) generate the configuration with `plan -generate-config-out`. It then tidies that configuration and checks that it plans with no changes.

- **Clean code, not a dump:** references between resources instead of IDs, shared tags as locals, secrets as variables, and clusters of resources as calls of curated modules.
- **Checked, not hoped:** a change to the generated code is kept only if the plan still shows no changes, every root goes through a verification gate (format, validate, a plan with no changes, scanners, secrets, determinism), and the report says what was imported, what was left out and why.
- **Read-only:** it never applies anything and writes no state; least-privilege [permission templates](docs/permissions.md) for each cloud.
- **AWS, Azure and Google Cloud**, with Terraform or OpenTofu, from the command line, a [GitHub Action](action/README.md) or an [AI agent over MCP](docs/mcp.md).

Terraform's own [`terraform query`](https://developer.hashicorp.com/terraform/cli/commands/query) and HCP Terraform's Search & Import suit a handful of resources; infraharvest is for whole accounts, subscriptions and projects (see [infraharvest and Terraform's own import](docs/index.md#infraharvest-and-terraforms-own-import)).

Licensed under [AGPL-3.0-only](LICENSE). The documentation is also published at [ignatg.github.io/infraharvest](https://ignatg.github.io/infraharvest/).

## Documentation

- [Quickstart](docs/quickstart.md): list, choose, import and plan, on AWS, Azure or Google Cloud.
- [Install](docs/install.md): releases, the container image, the install script, Go.
- [Usage](docs/usage.md): commands, choosing what to import, services and filters, day-to-day runs, and which options go together.
- [Audit](docs/audit.md): what in an account no Terraform state or other tool manages, without importing anything.
- [Configuration](docs/configuration.md): the configuration file and the state backend.
- [Output](docs/output.md): what each root holds, applying it, how the configuration is tidied, the report and the verification gate.
- [Modules](docs/modules.md): the curated modules clusters of resources move into, your own modules, and generated local modules.
- [Permissions](docs/permissions.md): read-only access for AWS, Azure and Google Cloud.
- [Troubleshooting](docs/troubleshooting.md): what an error message or exit code means, and what to do.
- [GitHub Action](action/README.md): scheduled imports that open a pull request.
- [AI agents (MCP)](docs/mcp.md) · [Catalogue](docs/catalogue.md) · [Command-line reference](docs/reference/cli.md)

## Install

On Linux and macOS, including AWS CloudShell, Azure Cloud Shell and Google Cloud Shell once cosign is there ([Install](docs/install.md#in-a-cloud-shell) shows how):

```sh
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
```

The script installs the latest release into `~/.local/bin`, after checking its [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) signature and its checksum. The other ways are in [Install](docs/install.md):

- a [release](https://github.com/IgnatG/infraharvest/releases) archive for Linux, macOS or Windows, on amd64 or arm64: on Windows, use the zip;
- the container image `ghcr.io/ignatg/infraharvest`, with Terraform and OpenTofu included;
- `go install github.com/IgnatG/infraharvest@latest`, with Go 1.27 or later;
- the [GitHub Action](action/README.md), in a workflow.

infraharvest needs Terraform 1.5 or later, which it downloads if there is none on `PATH`, or OpenTofu 1.6 or later with `--engine=tofu`. It also needs credentials that can read the resources: the [permission templates](docs/permissions.md) grant read-only access on each cloud. `infraharvest version` checks the install.

## Import

The steps are the same on every cloud:

1. `infraharvest discover <provider>` lists the resources into `selection.yaml`, and opens the picker on it in a terminal. By default it leaves out what the cloud creates itself and what another tool manages, with the reason.
2. Review `selection.yaml`: each resource has `include: true` or `false`. `infraharvest pick --selection selection.yaml` opens the picker again.
3. `infraharvest import <provider>`, with the same flags and `--selection=selection.yaml`, writes a root for each account (subscription, project) and region under `infraharvest-out/`, and a report in `infraharvest-out/report/`.

`--all` in place of `--selection` imports everything the default selection includes, without a file. See the [Quickstart](docs/quickstart.md) and [Usage](docs/usage.md).

### AWS

```sh
infraharvest discover aws --profile prod --services=vpc,subnet,sg,s3 --regions=eu-west-2,us-east-1
infraharvest import aws --profile prod --services=vpc,subnet,sg,s3 --regions=eu-west-2,us-east-1 --selection=selection.yaml
```

For several accounts, add `--accounts=111111111111,222222222222` or `--organization`: each account is reached through the role `--assume-role` names. See [AWS](docs/aws.md).

### Azure

```sh
export ARM_SUBSCRIPTION_ID=<subscription ID>
infraharvest discover azure --services=resource_graph --resource-group=web
infraharvest import azure --services=resource_graph --resource-group=web --selection=selection.yaml
```

Without `--resource-group`, the import covers the whole subscription. For several, use `--subscriptions=<ID>,<ID>` or `--management-group=<group ID>`. See [Azure](docs/azure.md).

### Google Cloud

```sh
infraharvest discover google --services='*' --projects=my-project --regions=global,europe-west2
infraharvest import google --services='*' --projects=my-project --regions=global,europe-west2 --selection=selection.yaml
```

`--regions` is `global` by default; import `global` with your regions. For several projects, use `--projects=<ID>,<ID>`, `--organization=<organization ID>` or `--folders=<folder ID>,<folder ID>`. See [Google Cloud](docs/google.md).

## Apply the generated roots

infraharvest writes configuration and no state. Each root holds the generated configuration and `imports.tf`, with one `import` block per resource. You don't create or edit a state file first: the first `terraform apply` writes it.

```sh
cd infraharvest-out/aws/123456789012/eu-west-2
terraform init    # the backend in backend.tf, or local state without one
terraform plan    # imports every resource, changes nothing
terraform apply   # writes the resources into the state, changes nothing in the cloud
rm imports.tf     # the resources are in the state
```

- **State:** a `backend` (`s3`, `azurerm` or `gcs`) in the [configuration file](docs/configuration.md) gives each root a `backend.tf` with a state key of its own, and `infraharvest bootstrap --config <file>` writes a root that creates the storage. Without one, each root keeps local state in `terraform.tfstate`.
- **Secrets:** infraharvest never writes a secret value. Each secret becomes a `sensitive` variable in `variables.tf`, without a default: set it before planning, from your secret store or with `TF_VAR_<name>`. The root's README lists them.
- **Resources another state manages:** import with `--managed-state` pointing at that state, and they are left out, so no resource ends up in two states.
- **`rejected.hcl`:** what Terraform couldn't import, with its errors. Terraform doesn't load it: fix a resource and move its blocks back, or leave it out.

See [Applying the roots](docs/output.md#applying-the-roots).

## Day to day

| To | Use |
|---|---|
| Add new resources to the roots | `discover` again (it marks them `new`), review them, then `import` with `--selection` and `--incremental`; add `--managed-state` once `imports.tf` is deleted |
| Finish a run that failed part way | The same command with `--resume` |
| Generate the roots again | `--regenerate`; resources at other addresses get moved blocks, and those that go to another root it generates get removed blocks: apply the root they leave first |
| Import what `discover` just listed | `--reuse-inventory`, instead of listing again |
| Check roots after editing them | `infraharvest verify`, before deleting `imports.tf` |
| See what changed in the cloud since applying | `infraharvest drift`: which changes the next apply would revert (see [Drift](docs/drift.md)) |
| Print the report again | `infraharvest report`, with `--format json` for JSON |
| Import faster | `--parallel` for accounts and regions (default 1), `--list-parallel` for services (default 4) |
| Lay out the roots | `--layout`, and `--env-tags` for roots per environment |
| Choose modules | `--modules`: `registry` (the default), `latest-untested`, `local` or `none`; your own modules in the configuration file |
| Import less | `--services`, `--skip`, `--id`, `--tag`, `--where` |
| Accept a partial import | `--allow-partial` |
| Script it | `--format json`: the report on stdout, logs on stderr |
| Run it in CI | The [GitHub Action](action/README.md) |
| Use it from an AI agent | `infraharvest mcp` (see [AI agents](docs/mcp.md)) |

The exit code is 0 when everything was imported, 1 when something wasn't (3 with `--allow-partial`), 2 when the import couldn't run, and 130 or 143 when it was interrupted or terminated (see [Troubleshooting](docs/troubleshooting.md#exit-codes)). [Day to day](docs/usage.md#day-to-day) has more.

## Options that go together

- `import` needs `--selection` or `--all`, not both; `discover` refuses `--all`.
- `--services` is required, on the command line or in the configuration file.
- `--incremental` with `--resume` generates a root whose import didn't finish again, then adds to it.
- `--regenerate` or `--incremental`, not both; without either, an import into a root an earlier import finished is refused.
- `--incremental` finds what a root has from its import blocks or its checkpoint; without either, pass `--managed-state`.
- `--managed-state=backend` needs a backend in the configuration file.
- A `--layout` of your own starts with `{out}/`, contains `{account}` for several accounts, subscriptions or projects (on AWS, with any `--accounts` or `--organization`), and `{env}` with `--env-tags`.
- On AWS, `--accounts` or `--organization`, not both, and `--assume-role` with `{account}`.
- Azure's `--subscriptions` and `--management-group`, and Google Cloud's `--projects`, `--organization` and `--folders`, add up.

See [Options that go together](docs/usage.md#options-that-go-together) for the full list.

## How it works

1.  The provider's listers call the cloud's APIs and record each resource's type, ID and attributes. The [catalogue](docs/catalogue.md) turns them into the ID Terraform imports the resource by, and says which resources the default selection leaves out.
2.  The selection decides which of them to import (see [Choosing what to import](docs/usage.md#choosing-what-to-import)).
3.  infraharvest writes an `import` block for each one into a root, and runs `terraform plan -generate-config-out` (or `tofu`), so the provider itself reads every resource and writes its configuration.
4.  It tidies the generated configuration: references between resources, shared tags and identifiers as locals, secrets as variables, and clusters of resources as module calls. Each change is kept only if a new plan shows no extra changes.
5.  It runs the verification gate on every root and writes a report.

infraharvest reads the cloud and writes files; it never applies anything, and writes no state. `terraform apply` on a generated root records the imported resources in state without changing them.

## Supported providers

- [AWS](docs/aws.md)
- [Azure](docs/azure.md)
- [Google Cloud](docs/google.md)

Each is covered in depth: inventory APIs, the catalogue, curated modules, and end-to-end tests on an emulator on every pull request. Each is also tested against a real AWS account, Azure subscription and Google Cloud project with the read-only permission templates. A new provider is added by following the steps below.

## Adding a provider

1.  Create `internal/providers/<name>/` with the provider and a lister for each of its services. The provider implements `harvest.Provider` (`internal/harvest/provider.go`), and `Services` maps each service's name to its lister, which usually embeds `harvest.ListerBase`. A lister's `List` lists the service's resources and records each one with `harvest.Record` or `harvest.RecordBare`: its ID, a name, the Terraform resource type, and attributes for import IDs and selection rules.
2.  Create `internal/cli/providers/provider_cmd_<name>.go`, under the build constraint `!minimal || <name>`. It describes the provider's command with a `definition` (see `command.go`): the help, the provider's own flags, and the planner that turns them into the scopes and calls to import. It registers it with `define` from `init()`.
3.  Describe the provider in its `Profile` (`internal/harvest/profile.go`): its command's name, its registry `Source` when its Terraform provider isn't `hashicorp/<name>`, how it applies tags, which files of a root its resources go in, and what a root that environments share reads of its account and region. Implement the optional interfaces in `internal/harvest/capabilities.go` that it needs, for example `ProviderWithScope` to lay out roots by account and region, or `ProviderWithTerraformEnv` to pass credentials on to Terraform.
4.  Add catalogue entries under `internal/catalogue/data/<provider>/` for types that need them, such as an import ID template when the ID a lister records isn't the one Terraform imports by, each with a golden test (see [docs/catalogue.md](docs/catalogue.md)).
5.  Add a page under `docs/`, list it in `mkdocs.yml`'s navigation, and link it from this README. To have the [command-line](docs/reference/cli.md) and [services](docs/reference/services.md) references cover it, add it to `referenceProviders` in `internal/cli/clireference_test.go` and rewrite them with the `golden` task of the `remote` workflow (see [CONTRIBUTING.md](CONTRIBUTING.md#documentation)).

## Contributing

Contributions are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) describes the workflow: branches and pull requests against `main`, commit messages, the checks CI runs, and how to rewrite golden files and generated docs on GitHub without a local Go toolchain. [docs/development.md](docs/development.md) is the reference for how the code is written: the layout, the conventions, the tests and the checks behind them. The [documentation](docs/index.md) explains how infraharvest works from the user's side. Report vulnerabilities privately, as [SECURITY.md](SECURITY.md) says, and follow the [code of conduct](CODE_OF_CONDUCT.md).

## Licence

infraharvest is licensed under the [GNU Affero General Public License, version 3 only](LICENSE) (AGPL-3.0-only). The schema metadata it embeds, the defaults and argument constraints read out of the HashiCorp Terraform providers' code (`internal/catalogue/schemameta/*.json.gz`), is MPL-2.0, as those providers are ([LICENSES/MPL-2.0.txt](LICENSES/MPL-2.0.txt)): [NOTICE](NOTICE) names the releases it was read from and where their source is (see [Licences](docs/catalogue.md#licences)).

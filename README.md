# infraharvest

infraharvest generates Terraform configuration for infrastructure that already exists. It lists your resources, writes an `import` block for each, and has Terraform (or OpenTofu) generate the configuration with `plan -generate-config-out`. It then tidies that configuration and checks that it plans with no changes.

> **Work in progress.** infraharvest is under active development, and its commands and output may still change before the first release.

Licensed under [AGPL-3.0-only](LICENSE).

## Documentation

- [Quickstart](docs/quickstart.md): list, choose, import and plan, on AWS, Azure or Google Cloud.
- [Install](docs/install.md): releases, the container image, the install script, Go.
- [Usage](docs/usage.md): commands, choosing what to import, services and filters.
- [Configuration](docs/configuration.md): the configuration file and the state backend.
- [Output](docs/output.md): what each root holds, how the configuration is tidied, the report and the verification gate.
- [Modules](docs/modules.md): the curated modules clusters of resources move into, your own modules, and generated local modules.
- [Permissions](docs/permissions.md): read-only access for AWS, Azure and Google Cloud.
- [AI agents (MCP)](docs/mcp.md) · [Catalogue](docs/catalogue.md) · [Command-line reference](docs/reference/cli.md)

## How it works

1.  The provider's listers call the cloud's APIs and record each resource's type, ID and attributes. The [catalogue](docs/catalogue.md) turns them into the ID Terraform imports the resource by, and says which resources the default selection leaves out.
2.  The selection decides which of them to import (see [Choosing what to import](docs/usage.md#choosing-what-to-import)).
3.  infraharvest writes an `import` block for each one into a root, and runs `terraform plan -generate-config-out` (or `tofu`), so the provider itself reads every resource and writes its configuration.
4.  It tidies the generated configuration: references between resources, shared tags and identifiers as locals, secrets as variables, and clusters of resources as module calls. Each change is kept only if a new plan shows no extra changes.
5.  It runs the verification gate on every root and writes a report.

infraharvest reads the cloud and writes files; it never applies anything, and writes no state. `terraform apply` on a generated root records the imported resources in state without changing them.

## Installation

From a [release](https://github.com/IgnatG/infraharvest/releases) (signed with cosign), the container image `ghcr.io/ignatg/infraharvest`, the install script for Linux, macOS and cloud shells, or Go:

```sh
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
go install github.com/IgnatG/infraharvest@latest
```

The install script verifies the release's signature with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/), which AWS CloudShell, Azure Cloud Shell and Google Cloud Shell don't include: [Install](docs/install.md#in-a-cloud-shell) shows how to add it there.

infraharvest needs Terraform 1.5 or later, which it downloads if there is none on `PATH`, or OpenTofu 1.6 or later. See [Install](docs/install.md) for verifying a release, the image, and building from source.

## Quickstart

```sh
infraharvest discover aws --services=vpc,subnet,sg,s3 --regions=eu-west-2    # writes selection.yaml to review
infraharvest import aws --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
cd infraharvest-out/aws/<account>/eu-west-2 && terraform init && terraform plan   # imports, changes nothing
```

See the [Quickstart](docs/quickstart.md) for Azure and Google Cloud.

## Supported providers

- [AWS](docs/aws.md)
- [Azure](docs/azure.md)
- [Google Cloud](docs/google.md)

Each is covered in depth: inventory APIs, the catalogue, curated modules and end-to-end tests on an emulator. A new provider is added by following the steps below.

## Adding a provider

1.  Create `internal/providers/<name>/` with the provider and a lister for each of its services. The provider implements `harvest.Provider` (`internal/harvest/provider.go`), and `Services` maps each service's name to its lister, which usually embeds `harvest.ListerBase`. A lister's `List` lists the service's resources and records each one with `harvest.Record` or `harvest.RecordBare`: its ID, a name, the Terraform resource type, and attributes for import IDs and selection rules.
2.  Create `internal/cli/providers/provider_cmd_<name>.go`, under the build constraint `!minimal || <name>`. It describes the provider's command with a `definition` (see `command.go`): the help, the provider's own flags, and the planner that turns them into the scopes and calls to import. It registers it with `define` from `init()`.
3.  Implement the optional interfaces in `internal/harvest/capabilities.go` that the provider needs, for example `ProviderWithRegistrySource` when its Terraform provider isn't `hashicorp/<name>`, `ProviderWithScope` to lay out roots by account and region, or `ProviderWithTerraformEnv` to pass credentials on to Terraform.
4.  Add catalogue entries under `internal/catalogue/data/<provider>/` for types that need them, such as an import ID template when the ID a lister records isn't the one Terraform imports by, each with a golden test (see [docs/catalogue.md](docs/catalogue.md)).
5.  Add a page under `docs/`, list it in `mkdocs.yml`'s navigation, and link it from this README. To have the [command-line](docs/reference/cli.md) and [services](docs/reference/services.md) references cover it, add it to `referenceProviders` in `internal/cli/clireference_test.go` and rewrite them with the `golden` task of the `remote` workflow (see [CONTRIBUTING.md](CONTRIBUTING.md#documentation)).

## Contributing

Contributions are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) describes the workflow: branches and pull requests against `main`, commit messages, the checks CI runs, and how to rewrite golden files and generated docs on GitHub without a local Go toolchain. [docs/development.md](docs/development.md) is the reference for how the code is written: the layout, the conventions, the tests and the checks behind them. The [documentation](docs/index.md) explains how infraharvest works from the user's side. Report vulnerabilities privately, as [SECURITY.md](SECURITY.md) says, and follow the [code of conduct](CODE_OF_CONDUCT.md).

## Licence

infraharvest is licensed under the [GNU Affero General Public License, version 3 only](LICENSE) (AGPL-3.0-only).

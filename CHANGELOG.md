# Changelog

All notable changes to infraharvest are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0.0, a minor release may change commands, flags and output.

## [0.1.0] - Unreleased

The first release. infraharvest generates Terraform configuration for infrastructure that already exists: it lists your resources, writes an `import` block for each, has Terraform or OpenTofu generate the configuration with `plan -generate-config-out`, tidies it, and checks that it plans with no changes. It reads the cloud and writes files; it never applies anything and writes no state.

### Added

- **Three clouds.** `import` and `discover` for AWS, Azure and Google Cloud, each with its cloud's inventory API: AWS Resource Explorer to skip services a region doesn't use, and the Cloud Control API for types without a dedicated lister; Azure Resource Graph; Google Cloud Asset Inventory. `infraharvest services <provider>` lists what each provider covers, and the [services reference](https://ignatg.github.io/infraharvest/reference/services/) names every Terraform type.
- **Whole estates.** Every account of an AWS organization (assuming a read-only role in each), every subscription under an Azure management group, and every project of a Google Cloud organization or folder, each into roots of its own and several at once with `--parallel`. A scope that fails is reported while the run goes on.
- **Terraform and OpenTofu.** `--engine=terraform` (1.5 or later, downloaded and verified when none is on `PATH`) or `--engine=tofu` (1.6 or later); the output plans with no changes on both.
- **Choosing what to import.** `discover` writes a selection file to review, and `pick` opens it in a terminal picker. The default selection leaves out what the cloud creates itself and what CloudFormation stacks manage, with the reason. Filters by service, ID, tag and attribute; `--managed-state` leaves out what existing Terraform state already manages, read from files, S3, Cloud Storage, Azure Blob Storage or HCP Terraform.
- **Configuration people would write.** References between resources instead of hard-coded IDs, data sources for resources the selection leaves out, shared tags and labels as a local applied through the provider where it can, repeated identifiers as locals, secrets as variables, and optional arguments that only repeat a default left out. Each change is kept only if a new plan shows no extra changes.
- **Modules.** Clusters of resources move into calls of curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules) at exact, tested versions, into your own modules, or into generated local modules. A call is kept only if its resources plan as they did in the root.
- **The verification gate.** Every root is checked for formatting, validation, a plan that imports everything and changes nothing else, the output standard, scanners (tflint, trivy, checkov when installed), secrets in written files, and determinism. `infraharvest verify` runs it again on generated roots.
- **A report.** `coverage.json`, `manifest.json` and `report.md` record what was found, imported, left out and why, with the versions used; `--format json` prints it to standard output. Exit codes tell a complete import from a partial or failed one.
- **Re-runs.** `--resume` continues a run that failed part way, `--incremental` adds what is new to roots already in use, and `--reuse-inventory` imports from what `discover` listed.
- **State backends.** A configuration file sets flags and the S3, azurerm or GCS backend of the generated roots; `infraharvest bootstrap` writes a root that creates the state storage.
- **The catalogue.** What infraharvest knows about each resource type, as YAML data with a JSON schema: import IDs, skip rules, child resources, data sources and arguments to omit. `--catalogue` adds your own entries on top of the built-in ones.
- **AI agents.** `infraharvest mcp` serves `discover`, `import` and `report` over the Model Context Protocol, and asks the user to confirm before an import.
- **GitHub Action.** `IgnatG/infraharvest/action` installs a verified release, runs an import in a workflow, adds the report to the job summary, and can open a pull request with the configuration.
- **Read-only permissions.** Terraform configurations under `permissions/` grant the minimum read-only access for AWS, Azure and Google Cloud.
- **Signed releases.** Archives for Linux, macOS and Windows on amd64 and arm64, a checksum file signed with cosign (keyless, from the release workflow), SBOMs, build provenance, and a signed container image, `ghcr.io/ignatg/infraharvest`, with Terraform and OpenTofu included. `install.sh` verifies the signature before installing.

[0.1.0]: https://github.com/IgnatG/infraharvest/releases

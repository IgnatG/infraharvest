# infraharvest GitHub Action

Runs [infraharvest](../README.md) in a workflow. It generates Terraform configuration for existing cloud resources, verifies that the configuration plans with no changes, and adds the import report to the job summary. It can also open a pull request with the configuration, with the report as its body.

The action installs a release and verifies it before running anything. First it checks that this repository's release workflow signed the checksum file, using cosign keyless signing; then it checks the archive's checksum. Cloud credentials come from the environment, so use OIDC rather than stored secrets. The action reads the cloud and writes files; it never applies anything.

## Example: a scheduled import that opens a pull request

```yaml
name: import
on:
  schedule:
    - cron: "0 6 * * 1"
  workflow_dispatch:

permissions:
  contents: write       # push the pull request branch
  pull-requests: write  # open the pull request
  id-token: write       # OIDC to AWS

jobs:
  import:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<sha>
      - uses: aws-actions/configure-aws-credentials@<sha>
        with:
          role-to-assume: arn:aws:iam::123456789012:role/infraharvest-read-only
          aws-region: eu-west-2
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.2.0
          provider: aws
          resources: vpc,subnet,sg,s3,iam
          regions: eu-west-2
          selection: selection.yaml
          pull-request: "true"
```

The role only needs read access: see [permissions/aws](../permissions/aws).

Run again on a schedule, the action adds what is new to the roots the repository already has (`incremental`). New resources go into `generated_2.tf`, `generated_3.tf` and so on, and nothing the roots have changes. The action finds a root's resources from its import blocks. Once you have applied a root and deleted its `imports.tf`, set `managed-state` too, for example `backend` with a configuration file (`config`) that names the S3 state backend.

## Inputs

| Input | Default | Description |
|---|---|---|
| `version` | | The release to install, such as `v0.2.0`. Either `version` or `binary` is required |
| `binary` | | An infraharvest binary to run instead of installing a release |
| `token` | `github.token` | Token to download the release with |
| `provider` | | The provider command, such as `aws` (required) |
| `resources` | | Services to import, comma-separated (required) |
| `regions` | | Regions, comma-separated |
| `selection` | | A selection file from `infraharvest discover`. Without one, the default selection is imported (`--all`) |
| `engine` | `terraform` | `terraform` or `tofu` |
| `output` | `generated` | Directory for the configuration and the report |
| `config` | | An infraharvest configuration file, for example with the state backend |
| `incremental` | `true` | Add what is new to the roots `output` already has, in files of their own (`--incremental`), instead of failing on them |
| `managed-state` | | State that says what Terraform already manages (`--managed-state`), such as `backend` |
| `allow-partial` | `false` | Succeed with exit code 3 when some resources couldn't be imported |
| `pull-request` | `false` | Open or update a pull request with the output |
| `pull-request-branch` | `infraharvest/import` | The pull request's branch |

## Outputs

| Output | Description |
|---|---|
| `exit-code` | infraharvest's exit code: 0 complete, 1 incomplete, 2 couldn't run, 3 partial |
| `report` | Path of the import report |
| `pull-request-url` | URL of the pull request, if one was opened |

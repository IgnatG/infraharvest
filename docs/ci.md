# GitLab CI and Azure Pipelines

infraharvest has a [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md) for GitHub, and templates for GitLab CI and Azure Pipelines that do the same core job: a scheduled import on the default branch that opens a merge request (pull request) with the generated configuration, and keeps the report. The templates don't run [drift](drift.md) or send to a [control plane](export.md): run `infraharvest drift` or `infraharvest export` as further steps where you need them.

| | GitLab CI | Azure Pipelines |
| --- | --- | --- |
| Template | [`ci/gitlab/infraharvest.gitlab-ci.yml`](https://github.com/IgnatG/infraharvest/blob/main/ci/gitlab/infraharvest.gitlab-ci.yml) | [`ci/azure-pipelines/infraharvest.yml`](https://github.com/IgnatG/infraharvest/blob/main/ci/azure-pipelines/infraharvest.yml) |
| How infraharvest is installed | the release, by [`install.sh`](install.md#with-the-install-script) in `alpine`, which checks its cosign signature and checksum | the release image of the provider's cloud, `ghcr.io/ignatg/infraharvest:<release>-<cloud>`, after `cosign verify` |
| The report | printed in the job log, and kept as an artifact | the run's summary |
| The change | a merge request from `infraharvest/import`, opened with a push option | a pull request in Azure Repos from `infraharvest/import`, opened with `az repos` |

Pin both to a release tag: the template, and the infraharvest release it runs.

## Run it on code you trust

As with the Action, the job runs Terraform's providers and modules, and reads the repository's selection and configuration files, with the job's cloud credentials. So run it only on the default branch, on a schedule or by hand, never on a merge request's or pull request's content: the GitLab template's rule allows only the default branch's scheduled and manual (`web`) pipelines, and the Azure Pipelines example sets `trigger: none` and `pr: none`. Trust only the default branch in the cloud's OIDC trust: in GitLab, the subject `project_path:<group>/<project>:ref_type:branch:ref:main`; in Azure DevOps, the service connection, which you can restrict to the pipeline.

## GitLab CI

Include the template at a release tag and extend `.infraharvest-import`:

```yaml
include:
  - remote: https://raw.githubusercontent.com/IgnatG/infraharvest/v0.1.0/ci/gitlab/infraharvest.gitlab-ci.yml

infraharvest:
  extends: .infraharvest-import
  id_tokens:
    INFRAHARVEST_AWS_ID_TOKEN:
      aud: sts.amazonaws.com
  variables:
    INFRAHARVEST_VERSION: v0.1.0
    INFRAHARVEST_PROVIDER: aws
    INFRAHARVEST_ARGS: --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
    AWS_ROLE_ARN: arn:aws:iam::123456789012:role/infraharvest
```

Then add a pipeline schedule for the default branch.

| Variable | |
| --- | --- |
| `INFRAHARVEST_VERSION` | The release, such as `v0.1.0`. Required. |
| `INFRAHARVEST_PROVIDER` | `aws`, `azure` or `google`. |
| `INFRAHARVEST_ARGS` | The import's flags, as on the command line: the services, the scope, and `--selection=<file>` or `--all`. They are split at spaces and not globbed, so `--services=*` needs no quotes. |
| `INFRAHARVEST_OUT` | Where the roots and the report go; `infrastructure` by default. |
| `INFRAHARVEST_ALLOW_PARTIAL` | `true`: a partial import (exit code 3, with `--allow-partial` in the flags) still opens the merge request. |
| `INFRAHARVEST_MR_TOKEN` | A project access token with the `write_repository` scope, as a masked, protected CI/CD variable. Without one, the job imports and keeps the result as an artifact, and opens no merge request. |
| `INFRAHARVEST_MR_BRANCH` | The branch the merge request comes from; `infraharvest/import` by default. Each run replaces it. |

Pipelines that GitLab starts for a push made with a project access token run as usual, so your checks (`terraform plan`, tflint, policy checks) run on the merge request.

Credentials, with GitLab's ID tokens:

- **AWS:** an ID token `INFRAHARVEST_AWS_ID_TOKEN` with the audience `sts.amazonaws.com`, and `AWS_ROLE_ARN`. The template writes the token to a file and sets `AWS_WEB_IDENTITY_TOKEN_FILE`, which the SDK reads. The role trusts GitLab's OIDC provider (`https://gitlab.com`, or your instance).
- **Azure:** an ID token named `ARM_OIDC_TOKEN` with the audience `api://AzureADTokenExchange`, and `ARM_USE_OIDC=true`, `ARM_TENANT_ID`, `ARM_CLIENT_ID` and `ARM_SUBSCRIPTION_ID` (see [Azure](azure.md)). The app registration or managed identity has a federated credential for the project and branch.
- **Google Cloud:** an ID token `INFRAHARVEST_GOOGLE_ID_TOKEN` with the workload identity provider's audience. The template writes it to `/tmp/infraharvest-google-token`; create the credential configuration with `gcloud iam workload-identity-pools create-cred-config ... --credential-source-file=/tmp/infraharvest-google-token`, commit it, and set `GOOGLE_APPLICATION_CREDENTIALS` to its path.

The template installs into `alpine:3.24`, with Alpine's `cosign`, and lets infraharvest download Terraform. For OpenTofu, add `--engine=tofu` to `INFRAHARVEST_ARGS` and install OpenTofu in a `before_script` of your job. For a runner without internet access, mirror the release and point `INFRAHARVEST_BASE_URL` at it, as `install.sh` reads it.

## Azure Pipelines

The template is a list of steps. Add this repository as a repository resource at a release tag, and use it from a pipeline in the repository that holds the roots:

```yaml
resources:
  repositories:
    - repository: infraharvest
      type: github
      name: IgnatG/infraharvest
      ref: refs/tags/v0.1.0
      endpoint: <GitHub service connection>

schedules:
  - cron: "0 5 * * 1"
    branches: { include: [main] }
    always: true
trigger: none
pr: none

pool: { vmImage: ubuntu-latest }
steps:
  - checkout: self
    persistCredentials: true
  - template: ci/azure-pipelines/infraharvest.yml@infraharvest
    parameters:
      version: v0.1.0
      provider: azure
      args: --services=resource_graph --resource-group=web --selection=selection.yaml
      env:
        ARM_USE_OIDC: "true"
        ARM_TENANT_ID: <tenant ID>
        ARM_CLIENT_ID: <client ID of the service connection's identity>
        ARM_SUBSCRIPTION_ID: <subscription ID>
        ARM_ADO_PIPELINE_SERVICE_CONNECTION_ID: <service connection ID>
        SYSTEM_ACCESSTOKEN: $(System.AccessToken)
```

| Parameter | |
| --- | --- |
| `version` | The release, such as `v0.1.0`. Required. |
| `provider` | `aws`, `azure` or `google`. |
| `args` | The import's flags, as for GitLab's `INFRAHARVEST_ARGS`. |
| `out` | Where the roots and the report go; `infrastructure` by default. |
| `allowPartial` | `true`: a partial import (exit code 3) still opens the pull request. |
| `pullRequest` | `false` to only import. With `true`, the default, the project's build service needs Contribute, Create branch and Contribute to pull requests on the repository, and the checkout `persistCredentials: true`. |
| `pullRequestBranch` | The branch the pull request comes from; `infraharvest/import` by default. |
| `env` | The cloud's credentials, as environment variables, which the template passes into the container by name. Map secret variables here: they don't reach steps otherwise. |
| `cosignImage` | The cosign image that checks the release image's signature. |

The import runs in the release image, as the agent's user, with the checkout mounted at `/work`: a file a credential names, such as `GOOGLE_APPLICATION_CREDENTIALS`, must be in the checkout and named by its path in `/work`. With `ARM_ADO_PIPELINE_SERVICE_CONNECTION_ID` and `SYSTEM_ACCESSTOKEN`, infraharvest and the azurerm provider ask the pipeline for the service connection's OIDC token themselves (see [Azure](azure.md)). For AWS, pass `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` from a step that assumes the role, such as the AWS Toolkit's, or `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE` with the token file in the checkout.

The pull request step works with Azure Repos. For a repository on GitHub, use the [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md).

## Exit codes

Both templates fail the job when the import exits with anything but 0 (complete), or 3 (partial) with partial allowed, and then open nothing; the report is kept either way. See [Troubleshooting](troubleshooting.md) for what each exit code means.

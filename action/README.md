# infraharvest GitHub Action

Runs [infraharvest](../README.md) in a workflow. It generates Terraform configuration for existing cloud resources, verifies that the configuration plans with no changes, and adds the import report to the job summary. It can also check the roots the repository has for what changed in the cloud since they were applied ([drift](#drift)), and open a pull request with the configuration, with the reports as its body, or [commit it straight to a branch](#committing-straight-to-a-branch).

The action installs a release and verifies it before running anything. First it checks that this repository's release workflow signed the checksum file, using cosign keyless signing; then it checks the archive's checksum. Then, when importing, it checks the import as `infraharvest import <provider> --check` does, with the flags its inputs make and those the configuration file sets, so that an unknown provider or service, or a flag the import would refuse, fails the step before drift or the import reads anything. Cloud credentials come from the environment, so use OIDC rather than stored secrets. The action reads the cloud (and, for drift, the state) and writes files; it never applies anything.

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
          role-to-assume: arn:aws:iam::123456789012:role/infraharvest-readonly
          aws-region: eu-west-2
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.1.0
          provider: aws
          services: vpc,subnet,sg,s3,iam
          regions: eu-west-2
          selection: selection.yaml
          pull-request: "true"
```

The role only needs read access: [permissions/aws](../permissions/aws) creates it. To let this workflow assume it with its OIDC token, apply the template with the repository's subjects (`create_github_oidc_provider=true` too, if the account has no GitHub OIDC provider yet):

```sh
terraform apply -var 'github_oidc_subjects=["repo:acme/infra:ref:refs/heads/main"]'
```

A scheduled or manually dispatched run on the default branch has the subject `repo:<owner>/<repository>:ref:refs/heads/<branch>`; a job with a GitHub `environment` has `repo:<owner>/<repository>:environment:<name>` instead. A repository with immutable subject claims has `repo:<owner>@<owner ID>/<repository>@<repository ID>:` in place of `repo:<owner>/<repository>:`. `permissions/aws-drift` takes the same variables, but refuses `pull_request` and `repo:<owner>/<repository>:*` subjects, as it reads the state; and Azure and Google Cloud trust the workflow through a federated credential or a workload identity pool instead: see [Permissions](../docs/permissions.md).

## Run it on code you trust

The action takes what it runs from the repository the job checked out, not only from its inputs, so whoever can change that content can run commands with the job's cloud credentials, for an import as for [drift](#drift):

- The configuration file (`config`) can set any flag the import takes. `terraform-path` names a program the action runs, `modules` names modules Terraform installs and plans with (a module's `external` data source runs any program), `catalogue` changes what is generated and left out, and `managed-state` and a provider's `profile` choose which state and which credentials are read: on a runner whose AWS configuration has profiles, such as a self-hosted one, any of them, whose `credential_process` runs a command.
- The roots in `out`, which an incremental import adds to and verifies, and drift checks, run Terraform's init and plan with the providers and modules they name, and with any provider a `terraform.d` directory beside them holds.

So run the action on the default branch, on a schedule, a manual dispatch or a push, never on a pull request's content: not on `pull_request_target`, or `workflow_run`, with the pull request's head checked out, and not on `pull_request` with credentials. GitHub gives a `pull_request` run from a fork no secrets and no OIDC token, but a pull request from a branch of the repository runs that branch's workflow, so trust only the default branch's subject (`repo:<owner>/<repository>:ref:refs/heads/main`) in the cloud role, as [the example](#example-a-scheduled-import-that-opens-a-pull-request) passes it to the permissions template, never `repo:<owner>/<repository>:pull_request`.

What the action writes can't be led out of the repository by a link it commits, whatever runs it: the action refuses `out` or `drift-decisions` when it, or a directory it is in, is a symbolic link; infraharvest refuses a link under `out` to a directory, or to a file outside `out`; and the reports go into the job summary and the pull request only when they aren't read through a link. A `managed-state` source sends credentials only to its cloud's hosts: Blob Storage URLs must be on Azure's Blob Storage hosts, and a `tfc://` source's `?host=` gets only the token Terraform has for that host (`TF_TOKEN_<host>` or `terraform login`), none if it has none.

## Checks on the pull request

GitHub starts no workflows for a pull request opened with the default `github.token`, so your checks (a `terraform plan`, tflint, policy checks) never run on the import pull request, and required checks leave it unmergeable. For them to run, pass a GitHub App token, or a fine-grained personal access token with contents and pull requests write, as `pull-request-token`, for example from [actions/create-github-app-token](https://github.com/actions/create-github-app-token):

```yaml
      - uses: actions/create-github-app-token@<sha>
        id: app-token
        with:
          app-id: ${{ vars.IMPORT_APP_ID }}
          private-key: ${{ secrets.IMPORT_APP_PRIVATE_KEY }}
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          # the inputs above, and:
          pull-request: "true"
          pull-request-token: ${{ steps.app-token.outputs.token }}
```

With the default `github.token`, the repository's (or organisation's) setting **Allow GitHub Actions to create and approve pull requests**, under Settings, Actions, General, must be on, or opening the pull request fails; new repositories have it off.

Run again on a schedule, the action adds what is new to the roots the repository already has (`incremental`). New resources go after the blocks of the files of their types, such as `network.tf`, and nothing the roots have changes. The action finds a root's resources from its import blocks. Once you have applied a root and deleted its `imports.tf`, set `managed-state` too, for example `backend` with a configuration file (`config`) that names the S3 state backend.

Adding to a root runs Terraform on it: the import initialises the root and checks it with what it adds, which installs and runs the providers and modules the root names, with the job's cloud credentials, as [drift](#drift) does. Run an incremental import only on code you trust, such as the default branch on a schedule, not on a pull request's content, from a fork or from anyone who can open one.

A root environments share (see [Merging environments](../docs/configuration.md#merging-environments)) works the same way, although the job's checkout has no `.infraharvest/`, where an import records what it merged, as the generated `.gitignore` leaves it out: the action tells the merged root from its own files, generates each environment's roots again, and merges them again with what is new kept apart as its environment's, so that nothing the merged root has is imported twice or gets another address. Each run must import every environment of the stack: every account, subscription or project the configuration file maps to one. Once the import blocks are deleted, `managed-state` must read each environment's state, such as `backend`, which holds the state key each `vars/<env>/backend.hcl` names.

## Committing straight to a branch

With `commit: "true"` instead of `pull-request`, the action commits the output and pushes it, with no pull request: to `commit-branch`, or to the branch checked out without one. It commits what a pull request would carry, `out` and, with drift, the decisions file, with the same message (see [The pull request with drift](#the-pull-request-with-drift)), as git adds them: the `.gitignore` generated in `out` keeps what it ignores out of the commit, and nothing else the job changed goes in. The `commit-sha` output is the commit's SHA.

```yaml
permissions:
  contents: write       # push the commit
  id-token: write       # OIDC to AWS

jobs:
  import:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<sha>
      - uses: aws-actions/configure-aws-credentials@<sha>
        with:
          role-to-assume: arn:aws:iam::123456789012:role/infraharvest-readonly
          aws-region: eu-west-2
      - uses: IgnatG/infraharvest/action@<sha>
        id: infraharvest
        with:
          version: v0.1.0
          provider: aws
          services: vpc,subnet,sg,s3,iam
          regions: eu-west-2
          commit: "true"
          commit-branch: infraharvest/imported
```

- When the output hasn't changed, nothing is committed or pushed, and `commit-sha` is empty.
- The commit goes on top of the commit checked out, and the push never forces. If `commit-branch` has commits the checkout doesn't have, such as another run's, the push is rejected and the step fails: check out that branch (`ref` on `actions/checkout`) to add to it.
- A checkout with no branch, such as a pull request's or a tag's, needs `commit-branch`, or the step fails.
- The token is `pull-request-token`, `github.token` by default, which needs `contents: write`. GitHub starts no workflows for a push `github.token` makes; pass a GitHub App token or a fine-grained personal access token with contents write for them to run, as for [the pull request](#checks-on-the-pull-request).
- Branch protection rules and rulesets apply: a push to a branch that requires pull requests, such as `main` usually, is rejected, unless the token's app or user may bypass them.
- The commit's author is `github-actions[bot]`, unless the job sets `user.name` and `user.email` with `git config`.

## Drift

Once the roots are applied, people still change things in the cloud. With `drift` set, the action runs [`infraharvest drift`](../docs/drift.md) on the roots in `out` before importing: it refreshes each root once, with read access to its state, and reports which changes the next `terraform apply` would revert. The report, `drift.md`, goes into the job summary, and the `drift-found` output says whether drift is left.

| `drift` | What the action does |
|---|---|
| `off` (default) | No drift check |
| `report` | Finds the changes and writes the decisions file, `drift-decisions` or `drift.yaml` in `out`, with an entry per change to accept or keep, keeping the decisions it has. The code isn't changed, and the decisions file isn't applied |
| `accept` | Writes the cloud's values of the changes that `drift-decisions` (a decisions file you reviewed) or `drift-accept` (rules) accept into the code, once a plan of the edited code shows them taken; the others are kept, and the next apply reverts them. The decisions file is written again with this run's changes |

`drift-accept` takes one rule a line, as drift's `--rules` file does: `accept` or `keep`, then a rule as `--accept` takes it, such as `accept attribute=tags.*` or `keep type=aws_security_group`; a rule alone, such as `all`, is accepted, lines starting with `#` are skipped, and the last rule that matches a change decides (see [Accepting changes](../docs/drift.md#accepting-changes)). `drift-decisions` decides by the file first, and the rules after it. To review changes one by one, run with `drift: report`, commit the decisions file it writes, set each entry's `decision`, then run with `drift: accept` and `drift-decisions` naming that file. To keep the decisions file outside `out`, such as `decisions/drift.yaml`, set `drift-decisions` with `drift: report` too: it is written there, merged with what it has, and never applied in that mode.

Drift runs Terraform's init and plan on the roots in `out`, which install and run the providers and modules they name with the job's cloud credentials: a provider is a program, and the `external` data source runs any program the configuration names. Run drift only on code you trust, such as the default branch on a schedule (see [Run it on code you trust](#run-it-on-code-you-trust)). Running it on a pull request's content, from a fork or from anyone who can open one, runs that content's providers and modules with your credentials; the action, unlike the [MCP server](../docs/mcp.md), doesn't check which providers the roots use, and `--untrusted-roots`, which makes Terraform ignore their `.terraform` directories and filesystem mirrors, still runs the providers they name.

The drift check runs first, on the code the repository has, so that what the import adds isn't taken for drift; then the import adds the new resources (`incremental` must be `true`). One pull request carries both. A run with `import: "false"` only checks drift, and needs no `provider` or `services`. On a first run, before `out` exists, there is nothing to check, and the check is skipped.

`infraharvest drift` exits 4 when drift is left: that doesn't fail the step, and `drift-found` is `true`. Exit code 1 (an accepted change failed verification, so it wasn't written) and 2 (a root couldn't be checked, such as one without access to its state) fail the step, after adding the report to the job summary, and the import doesn't run. To fail the job on drift, as a check would, add a step after the action:

```yaml
      - name: Fail on drift
        if: steps.infraharvest.outputs.drift-found == 'true'
        run: |
          echo "::error::the cloud has changed since the roots were applied; see the job summary"
          exit 1
```

### The pull request with drift

With `drift` set, the pull request also covers the decisions file, wherever `drift-decisions` keeps it, and its body is the import report followed by `drift.md`:

| Run | Title | Commit | Default branch |
|---|---|---|---|
| Import only | Import existing resources with infraharvest | `feat: import existing resources with infraharvest` | `infraharvest/import` |
| `drift: accept` and import | Accept cloud changes (drift) and import new resources | `fix: accept drift found by infraharvest` | `infraharvest/import` |
| `drift: accept` only (`import: "false"`) | Accept cloud changes (drift) | `fix: accept drift found by infraharvest` | `infraharvest/drift` |
| `drift: report` and import | Import new resources, and record cloud changes (drift) for review | `feat: import new resources and record drift found by infraharvest` | `infraharvest/import` |
| `drift: report` only (`import: "false"`) | Record cloud changes (drift) for review | `chore: record drift found by infraharvest for review` | `infraharvest/drift` |

With `drift: report`, the pull request accepts nothing: it carries the decisions file and the drift report, for you to review the changes and decide each entry, and any import.

`drift.md` and `drift.json` redact secrets: values the provider marks sensitive, and values that look like credentials, are `(sensitive value)`. The action writes `drift.json` to a file in `RUNNER_TEMP` and to `out/report`, never to the log.

### Example: a daily drift check

```yaml
name: drift
on:
  schedule:
    - cron: "0 6 * * *"
  workflow_dispatch:

permissions:
  contents: write       # push the pull request branch
  pull-requests: write  # open the pull request
  id-token: write       # OIDC to AWS

# The same group as the workflow that applies the roots: drift doesn't lock
# the state, so it shouldn't read it while an apply writes it.
concurrency:
  group: terraform-apply
  cancel-in-progress: false

jobs:
  drift:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<sha>
      - uses: aws-actions/configure-aws-credentials@<sha>
        with:
          role-to-assume: arn:aws:iam::123456789012:role/infraharvest-drift
          aws-region: eu-west-2
      - uses: IgnatG/infraharvest/action@<sha>
        id: infraharvest
        with:
          version: v0.1.0
          import: "false"
          drift: accept
          drift-decisions: infraharvest-out/drift.yaml
          drift-accept: |
            attribute=tags.*
          pull-request: "true"
```

Set the roots' secret variables as `TF_VAR_<name>` in the job's `env`, from your secret store: a variable without a value gets a placeholder, and what it sets is reported as not assessable.

### Roots environments share

With `environments:` in the configuration file (`config`), the roots of each stack's environments [merge into one root](../docs/configuration.md#merging-environments), with what differs in `vars/<env>/`. Drift checks such a root once per environment, each time with the job's credentials, so the environments that aren't in the job's account, subscription or project would look deleted. Run a job per environment, with that environment's credentials, and `environment` naming it (`--environment`): a matrix does it. Drift fails, before checking anything, when `out` has roots of more than one environment and `environment` isn't set; set it to `all` (`--environment all`) when the job's credentials reach every environment, as when one account holds them all, split by tags.

Each job opens its own pull request, on `infraharvest/drift-<env>`, titled with the environment, such as "Accept cloud changes (drift) in prod". Give each environment a decisions file of its own, so that the pull requests don't change the same file:

```yaml
jobs:
  drift:
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        include:
          - environment: dev
            role: arn:aws:iam::111111111111:role/infraharvest-drift
          - environment: prod
            role: arn:aws:iam::222222222222:role/infraharvest-drift
    steps:
      - uses: actions/checkout@<sha>
      - uses: aws-actions/configure-aws-credentials@<sha>
        with:
          role-to-assume: ${{ matrix.role }}
          aws-region: eu-west-2
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.1.0
          import: "false"
          drift: report
          environment: ${{ matrix.environment }}
          drift-decisions: decisions/drift-${{ matrix.environment }}.yaml
          pull-request: "true"
```

The import of such roots can't run a job per environment: merging needs every environment's resources in one run. Run it in a job of its own, with `config` naming the environments and the accounts, subscriptions or projects to import (on AWS, `accounts` and `assume-role` under `providers.aws`), and credentials that can assume the role in each; `environment` doesn't go with `import: "true"`, but for `all`.

## Permissions for drift

Drift needs what your own `terraform plan` of the roots needs: the read-only access the import uses ([permissions/](../permissions)), to refresh the resources, and read access to each root's state, from the backend in its `backend.tf`. The templates `permissions/aws-drift`, `permissions/azure-drift` and `permissions/google-drift` grant both; the sections below say what they grant.

Every plan runs with `-lock=false`. Taking the state's lock writes to the backend (a `.tflock` object in S3 or GCS, a lease on the azurerm blob), and drift never writes the state, so read access is enough. The catch: a plan that doesn't lock can read the state while an apply is writing it, and report as drift what that apply is changing. Keep drift and your applies apart, such as with the same `concurrency` group as above, and if a report taken during an apply looks wrong, run it again once the apply has finished.

Drift also applies its refresh, in a private copy with local state only: that writes nothing to the backend or the cloud, but Terraform configures the provider again for it. With AWS, that calls `sts:GetCallerIdentity`, which every principal may call and no policy can deny; with Azure and Google Cloud, it signs in again. So the credentials must still be valid after the refresh, and the cloud's sign-in endpoints reachable from the runner, such as through an STS VPC endpoint whose policy allows the call: make the role's session last the whole job (`role-duration-seconds`, up to the role's `max_session_duration`).

### AWS: S3 backend

The role needs `s3:ListBucket` on the state bucket and `s3:GetObject` on the roots' state keys, `<key_prefix>/<root path>/terraform.tfstate` (see [Configuration](../docs/configuration.md)). Locking with `use_lockfile` needs nothing more, since drift doesn't lock; neither does a DynamoDB table, which the generated roots don't use. With the bucket `acme-terraform-state` and the key prefix `imported`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ListTheStateBucket",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::acme-terraform-state"
    },
    {
      "Sid": "ReadTheRootsState",
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::acme-terraform-state/imported/*"
    }
  ]
}
```

If the backend sets `kms_key_id`, the role needs `kms:Decrypt` on that key too, to read the encrypted state.

[permissions/aws-drift](../permissions/aws-drift) creates the role this section describes. The `infraharvest-readonly` role from [permissions/aws](../permissions/aws) explicitly denies `s3:GetObject` and `kms:Decrypt` everywhere ([`deny-data-reads.json`](../permissions/aws/deny-data-reads.json)), and an explicit deny wins over any allow. The state holds secrets, so give drift a role of its own, `infraharvest-drift` above: `ReadOnlyAccess`, the policy above, and a copy of `deny-data-reads.json` that leaves the three `s3:GetObject` actions and `kms:Decrypt` out of its first statement and denies them everywhere but the state instead:

```json
    {
      "Sid": "DenyObjectReadsButTheState",
      "Effect": "Deny",
      "Action": ["s3:GetObject", "s3:GetObjectTorrent", "s3:GetObjectVersion"],
      "NotResource": "arn:aws:s3:::acme-terraform-state/imported/*"
    },
    {
      "Sid": "DenyDecryptButTheStateKey",
      "Effect": "Deny",
      "Action": "kms:Decrypt",
      "NotResource": "arn:aws:kms:eu-west-2:123456789012:key/<the state key's ID>"
    }
```

Without `kms_key_id`, leave `kms:Decrypt` in the first statement and drop the second one here.

### Azure: azurerm backend

The `Reader` role from [permissions/azure](../permissions/azure) has no data actions, so it can't read the state's blob. Without other settings, the azurerm backend reads it with the storage account's access key, which needs `Microsoft.Storage/storageAccounts/listKeys/action` and gives full access to everything in the account. Read it with the identity's own token instead: set `ARM_USE_AZUREAD: "true"` in the job's `env`, and assign **Storage Blob Data Reader** on the state's container only:

```sh
az role assignment create \
  --assignee <object-id> \
  --role "Storage Blob Data Reader" \
  --scope "/subscriptions/<subscription>/resourceGroups/<resource-group>/providers/Microsoft.Storage/storageAccounts/<account>/blobServices/default/containers/<container>"
```

or in Terraform, an `azurerm_role_assignment` with `role_definition_name = "Storage Blob Data Reader"` and that `scope`. The backend's lock, a lease on the blob, needs write access, which drift doesn't take.

### Google Cloud: GCS backend

The role from [permissions/google](../permissions/google) can neither read nor list a bucket's objects: its IAM deny policy (`deny_data_reads`) denies `storage.objects.get` and `storage.objects.list`, which `roles/viewer` would otherwise allow through the bucket's convenience ACLs. A deny policy outranks any grant, so apply that template with `deny_data_reads = false` for a member that reads state, or use a separate member, as [permissions/google-drift](../permissions/google-drift) does. Grant **Storage Object Viewer** on the state bucket only:

```sh
gcloud storage buckets add-iam-policy-binding gs://acme-terraform-state \
  --member=serviceAccount:<email> \
  --role=roles/storage.objectViewer
```

It has `storage.objects.get`, to read `<prefix>/<root path>/default.tfstate`, and `storage.objects.list`, which the backend uses to find the workspaces. The backend's lock, a `default.tflock` object, needs `storage.objects.create`, which drift doesn't take. `permissions/google-drift` with `state_prefix` limits the reads to the objects under the prefix, through an IAM condition (see [Permissions](../docs/permissions.md#google-cloud-permissionsgoogle-drift)).

## Inputs

| Input | Default | Description |
|---|---|---|
| `version` | | The release to install, such as `v0.1.0`. Either `version` or `binary` is required |
| `binary` | | An infraharvest binary to run instead of installing a release |
| `token` | `github.token` | Token to download the release with |
| `import` | `true` | Import resources; `false` only checks [drift](#drift) |
| `provider` | | The provider command, such as `aws` (required when importing) |
| `services` | | Services to import, comma-separated. Required when importing, unless the `config` file sets them or `--cfn-stack` in `args` leaves them to the stacks |
| `regions` | | Regions, comma-separated |
| `selection` | | A selection file from `infraharvest discover`. Without one, the default selection is imported (`--all`) |
| `engine` | | `terraform` or `tofu`. Unset, the import and drift use what the configuration file sets, or `terraform` |
| `out` | `infraharvest-out` | Directory for the configuration and the report |
| `config` | | An infraharvest configuration file, for example with the state backend; drift takes its settings `engine`, `terraform-path`, `engine-timeout` and `catalogue`, and its own `modules` (`--config`) |
| `incremental` | `true` | Add what is new to the roots `out` already has, after the blocks of the file of their type (`--incremental`), instead of failing on them. Must be `true` with `drift` and an import |
| `args` | | More flags for the import, one a line, each starting with `--` (a bare `--` is refused): for Google Cloud, which names no project by default, `--projects=my-project` (or `--organization`, `--folders`); also `--resource-group`, `--layout` and the like |
| `managed-state` | | State that says what Terraform already manages (`--managed-state`), such as `backend` |
| `allow-partial` | `false` | Succeed with exit code 3 when some resources couldn't be imported |
| `drift` | `off` | `off`, `report` or `accept`: check the roots in `out` for changes in the cloud before importing (see [Drift](#drift)) |
| `drift-decisions` | | The decisions file. With `drift: report`, the file to write this run's changes into, without applying it (`--write-decisions=<file>`); with `drift: accept`, the file to decide the changes by (`--decisions`), written again with this run's changes. Without one, the decisions file is `drift.yaml` in `out` |
| `drift-accept` | | With `drift: accept`, rules for the changes to accept, one a line, such as `attribute=tags.*`; `keep <rule>` keeps what it matches, and the last rule that matches a change decides |
| `environment` | | For roots environments share, the environment to check drift as (`--environment`), with the job's credentials, which must be that environment's; several, comma-separated, share them, and `all` checks every one. Required when `out` has roots more than one environment shares; with `import: "true"`, only `all` (see [Roots environments share](#roots-environments-share)) |
| `pull-request` | `false` | Open or update a pull request with the output |
| `pull-request-branch` | `infraharvest/import`, or `infraharvest/drift` with `import: "false"` (`infraharvest/drift-<env>` with `environment`) | The pull request's branch |
| `pull-request-token` | `github.token` | Token to push the branch and open the pull request with, and to push with `commit`: see [Checks on the pull request](#checks-on-the-pull-request) |
| `commit` | `false` | Commit the output and push it to `commit-branch`, without a pull request (see [Committing straight to a branch](#committing-straight-to-a-branch)). Not with `pull-request` |
| `commit-branch` | the branch checked out | With `commit`, the branch to push to. A detached checkout, such as a pull request's, needs one |

## Outputs

| Output | Description |
|---|---|
| `exit-code` | infraharvest import's exit code: 0 complete, 1 incomplete, 2 couldn't run, 3 partial; empty with `import: "false"` |
| `report` | Path of the import report |
| `drift-found` | `true` when drift is left: a change the next apply would revert, or that can't be assessed. `false` when none is, or without `drift` |
| `drift-report` | Path of the drift report, `drift.md`, if drift ran |
| `drift-exit-code` | infraharvest drift's exit code: 0 no drift left, 4 drift left, 1 an accepted change failed verification, 2 a root couldn't be checked; empty if drift didn't run |
| `pull-request-url` | URL of the pull request, if one was opened |
| `commit-sha` | SHA of the commit pushed with `commit`; empty when the output didn't change, or without `commit` |

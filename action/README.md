# infraharvest GitHub Action

Runs [infraharvest](../README.md) in a workflow. It generates Terraform configuration for existing cloud resources, verifies that the configuration plans with no changes, and adds the import report to the job summary. It can also check the roots the repository has for what changed in the cloud since they were applied ([drift](#drift)), and open a pull request with the configuration, with the reports as its body, or [commit it straight to a branch](#committing-straight-to-a-branch), and can [draw the imported estate as a diagram](#diagram-of-the-imported-estate). Two more modes each do only one thing: `discover` lists the cloud into a selection file, and `verify` checks the roots the repository has again (see [Discover and verify](#discover-and-verify)).

The action installs the release's build for the `provider`'s cloud and verifies it before running anything. First it checks that this repository's release workflow signed the checksum file, using cosign keyless signing; then it checks the archive's checksum. Then, when importing, it checks the import as `infraharvest import <provider> --check` does, with the flags its inputs make and those the configuration file sets, so that an unknown provider or service, or a flag the import would refuse, fails the step before drift or the import reads anything. Cloud credentials come from the environment, so use OIDC rather than stored secrets. The action reads the cloud (and, for drift, the state) and writes files; it never applies anything.

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

## Discover and verify

Two more modes each do only one thing. Both need `import: "false"`, and neither goes with the other, `drift`, `pull-request`, `commit`, `sign-evidence` or `diagram`: the action refuses the combination before it installs anything.

`discover: "true"` runs `infraharvest discover <provider> --pick=false` with the `services`, `regions`, `config`, `args` and `allow-partial` inputs, as an import takes them, and `out`. It writes the selection file to `selection` (`selection.yaml` in the working directory without it), keeping the decisions in a file that is there, and sets the output `selection-file` to its path. It also saves what it listed in `out`, under `.infraharvest/inventory/`, which the OCSF inventory sent to a [control plane](#sending-a-run-to-a-control-plane) is made from. It reads the cloud only. The exit code is the import's: 0 when every listing worked, 1 when some failed in part (the file is still written, and says so), 2 when it couldn't run, and 3 with `allow-partial: "true"`, which doesn't fail the step.

```yaml
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.1.0
          provider: aws
          services: vpc,subnet,sg,s3
          regions: eu-west-2
          import: "false"
          discover: "true"
          selection: selection.yaml
```

`verify: "true"` runs `infraharvest verify <out>`, which runs the [verification gate](../docs/output.md#verification-gate) again on the roots already in `out`, with the `config` and `engine` inputs. The roots must be in the repository the job checked out: it fails if `out` isn't there. The plans read the cloud, so the job has its credentials as an import does, and the secret variables the roots need as `TF_VAR_` variables. It writes `_reports/verify.json` into `out`, and sets `exit-code` as verify exits: 0 when every check passed, 1 when one failed, 2 when a root couldn't be checked. Any other code than 0 fails the step.

```yaml
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.1.0
          provider: aws
          import: "false"
          verify: "true"
```

`provider` chooses the build to install when the action installs a release, for both, and is the provider to list for discover.

## Sending a run to a control plane

Off by default. With `control-plane-url` set, the action sends the metadata of the run to a hosted control plane, after the import, the drift check, the discover or the verify run (and after the pull request or commit): the documents `infraharvest export` writes, [the OCSF inventory, run, gate and graph](../docs/export.md) for an import, the drift summary for a drift check, the OCSF inventory and run for a discover run and the run and gate for a verify run, and nothing else. They hold no attributes, values or messages, and only the OCSF inventory and the drift summary have resource addresses (the OCSF inventory the paths of the roots too); the [Export](../docs/export.md) page lists what they have and what they leave out. The control plane has to implement the [Control plane API](../docs/export.md#control-plane-api).

```yaml
permissions:
  contents: read
  id-token: write       # OIDC to AWS, and the token the control plane is sent

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
        env:
          INFRAHARVEST_EXPORT_KEY: ${{ secrets.INFRAHARVEST_EXPORT_KEY }}
        with:
          version: v0.1.0
          provider: aws
          services: vpc,subnet,sg,s3,iam
          regions: eu-west-2
          control-plane-url: https://control-plane.example.com
          control-plane-drop-tag: owner-email
          control-plane-hash-ids: "true"
      - run: echo "sent run $RUN_ID"
        env:
          RUN_ID: ${{ steps.infraharvest.outputs.control-plane-run-id }}
```

- **Authentication.** The action requests the job's GitHub Actions OIDC token for the audience in `control-plane-audience` (default `infraharvest-api`; the control plane checks it, so set the one it expects) and trades it for a run token: `POST <url>/api/v1/session`, with an empty body and the OIDC token in the header `X-GitHub-OIDC-Token`. The control plane answers `{"token": "...", "expires_at": "..."}`, and the run token is the bearer token (`Authorization: Bearer`) of every upload. The OIDC token goes to the session endpoint only. The job needs `id-token: write`; without it the action says so and fails (before the import, when it can tell). Both tokens are masked and never printed, and go to `curl` in a file, not in its arguments.
- **Uploads.** The session is opened first, then the documents are exported and sent, one after another, to `<url>/api/v1/ingest/{ocsf-inventory|run|gate|graph|drift-summary}`. Each body is gzip-compressed (`Content-Encoding: gzip`, `Content-Type: application/json`), and carries `Idempotency-Key: <run_id>:<document>:<GITHUB_RUN_ID>`, so sending a document again in the same workflow run, or a re-run of it, gives the same result, while two workflow runs that find the same are still two runs. The OCSF inventory gets the `config` input, whose backend says where each root's state is. What is sent depends on what ran, as the next item says.
- **Refusals.** A `4xx` other than `429` is not retried. The step prints the reason the control plane gives in its JSON body, `{"error": "..."}`, or the start of the body if it isn't JSON. A `402` means the organisation's plan refused the upload, such as for being over its cloud-account limit: it is not a problem with the token. `401` and `403` mean the control plane refused the token: check `control-plane-audience`. The same goes for the session request.
- **Redaction.** `control-plane-drop-tags` and `control-plane-drop-tag` (a key a line, or comma-separated) leave tags out of the OCSF inventory, and `control-plane-hash-ids` replaces IDs and names (the graph's IDs and the addresses of the OCSF inventory, the graph and the drift summary too) with HMAC-SHA256 hashes under a key only you hold, from `control-plane-hash-key` or, better, `INFRAHARVEST_EXPORT_KEY` from a secret. `control-plane-drop-addresses` (`true` or `false`) leaves the addresses out of the OCSF inventory (with its module addresses and the paths of its roots), the graph (its nodes' addresses) and the drift summary instead, and wins when both are set. The OCSF inventory says what was done in its `redaction` block. Tags that look like secrets are always dropped. See [Export](../docs/export.md#what-it-leaves-out).
- **Transport.** HTTPS only (`http://` only for `localhost`, `127.0.0.1` and `[::1]`), no redirects followed. A `5xx`, a `429` and a connection failure are retried up to four times, with a growing delay; any other failure fails at once.
- **Failures.** The step runs last and after a failed import, drift check, discover or verify run too, when it left a report, so a failed run is reported as well. An export or upload failure fails the step, unless `control-plane-optional` is `true`, which turns it into a warning. The import, drift and verify steps remove the `report.json`, `drift.json` or `verify.json` an earlier run left in `out` (such as one a pull request committed) before they run, so a report is only ever sent for the run that wrote it. A run that left no report, such as an import that couldn't start, sends nothing, and a drift check that left no `drift.json` (`out` doesn't exist yet, or it failed before writing one) sends nothing, with a warning. So does a failed discover run that saved no inventory, and a failed verify run that left no `verify.json`; after a discover or verify step that succeeded, a missing file fails the step.
- **Times.** The step records when the import, discover or verify run started and finished (UTC) and passes them to `infraharvest export` (`--started-at`, `--finished-at`), so the run document has `started_at` and `finished_at`, and the finish is the `generated_at` of the OCSF inventory (the time of its events) and of the graph: sending the same run again changes none of them. A run with no recorded times, such as an import that never started, leaves them out. The drift check records its own: they go to the run document of a drift-only run, and its finish is the drift summary's `generated_at`.
- **Imports and drift.** An import (`import: "true"`) sends the OCSF inventory, run, gate and graph documents; the `inventory/v1` document isn't uploaded, and stays a document `infraharvest export` writes. A drift check (`drift: report` or `accept`) sends the drift summary, which counts the changes by root, outcome and resource type and lists the addresses of the resources that changed. With `import: "false"` and a drift check, the run document describes the drift check (`export --command drift`, from `drift.json`, so an import's older report in the repository isn't sent): the run document, then the drift summary, and nothing else. With both, the five are sent, the run document being the import's. The drift summary's `run_id` is the drift check's own, so its `Idempotency-Key` uses it. Without `control-plane-hash-ids` or `control-plane-drop-addresses`, the addresses are sent as they are; they can hold the key of a `for_each` resource. The OCSF inventory names the repository and the commit the configuration is in, from the job's `GITHUB_SERVER_URL`, `GITHUB_REPOSITORY` and `GITHUB_SHA`. An import with `incremental: "true"` (the default) sends the same documents, and its run document has the command `incremental` (`export --command incremental`) in place of `import`; it is the import's report, so the run document has the import's other fields too.
- **Discover and verify.** A discover run (`discover: "true"`) sends the OCSF inventory, then the run document (`export --command discover`), both made from the inventories discover saved in `out`; they take the same redaction flags as an import's. The inventories say nothing of who manages a resource, so every resource in the OCSF inventory is `unmanaged`, with the basis `none`, and has no `iac`: the selection file, which isn't sent, is where discover records which the default rules leave out. A verify run (`verify: "true"`) sends the run document (`export --command verify`), then the gate, both made from `_reports/verify.json`, which verify writes; the gate doesn't pass if a root couldn't be checked. Neither run has a graph or a drift summary. Their `run_id` is made from what they were made from, so the same results are the same run, and `control-plane-run-id` is the run document's.
- **Request ID.** `control-plane-request-id`, such as the ID a control plane gave to the request that started the run (see [Runs a control plane starts](#runs-a-control-plane-starts)), goes into the run document as `request_id` (`--request-id`), so the control plane can match the run to its request. It must be 1 to 128 letters, digits or `.` `_` `:` `-`; anything else fails before the run starts.
- **Sizes.** The step's log has one line for each document it exports, such as `control plane: the ocsf-inventory document is 48213 bytes, 3120 bytes gzipped`: the size in bytes of the JSON document and of the gzip-compressed body that is sent. The line has the document's name and the two numbers, nothing of its content, and is printed before the first upload, so a refused upload has them too. A sandbox run shows from them how large the documents get for an estate.
- **Output.** `control-plane-run-id` is the `run_id` of the run document (an import's, when the run has one), set once every document of the run was accepted.

### Runs a control plane starts

A control plane whose GitHub App can dispatch workflows on the repository can start an import, an incremental import, a drift check, a discover or a verify run itself, such as when someone asks for one from its pages. [`ci/github/infraharvest-dispatch.yml`](../ci/github/infraharvest-dispatch.yml) is the workflow for it: copy it to `.github/workflows/` on the default branch, set the repository variable `INFRAHARVEST_CONTROL_PLANE_URL`, and replace the credentials step of each job with your cloud's. It is started by `workflow_dispatch`, with three inputs:

- `command`: `import`, `incremental`, `drift`, `discover` or `verify`. Both `import` and `incremental` add what is new to the roots the repository already has, as the action does by default; `incremental` also sets `control-plane-command`, so that the run document says `incremental`.
- `inputs-ref`: the branch the control plane committed its files to, under `infraharvest/control-plane/` (the workflow refuses any other name, and the default branch): `selection.yaml` for an import, `drift.yaml` (the decisions to apply) for a drift check. Empty, the run has neither file, and a drift check only reports. A discover run needs it: the branch must exist, and the run commits the selection it lists to it as `selection.yaml`, which the control plane reads there.
- `request-id`: the control plane's ID of the request, checked as `control-plane-request-id` is and passed to it, so the run document sent back says which request it answers.

```yaml
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.1.0
          provider: aws
          import: ${{ inputs.command == 'import' || inputs.command == 'incremental' }}
          control-plane-command: ${{ inputs.command == 'incremental' && 'incremental' || '' }}
          selection: ${{ steps.files.outputs.selection }}
          drift: ${{ inputs.command == 'drift' && steps.files.outputs.drift || 'off' }}
          drift-decisions: ${{ steps.files.outputs.decisions }}
          pull-request: "true"
          control-plane-url: ${{ vars.INFRAHARVEST_CONTROL_PLANE_URL }}
          control-plane-request-id: ${{ inputs.request-id }}
```

The workflow has a job for each kind of run, so that each has only the permissions it needs, after a `check` job that checks the inputs and has none:

| `command` | Job | Permissions | What it does |
|---|---|---|---|
| `import`, `incremental`, `drift` | `infraharvest` | `contents: write`, `pull-requests: write`, `id-token: write` | Runs the action as the example above, and opens a pull request. It refuses a `selection.yaml` or `drift.yaml` that is a symbolic link, and passes the decisions in `drift.yaml` to a `drift` run only |
| `discover` | `discover` | `contents: write`, `id-token: write` | Runs `discover: "true"` with `selection` the file it checked out of `inputs-ref` (so the decisions in it stay), then commits the file back to `inputs-ref`, with no force, built with git's own commands so that nothing is written into a checkout. It refuses a `selection.yaml` that is a symbolic link. No commit if it didn't change. It changes no code and opens no pull request |
| `verify` | `verify` | `contents: read`, `id-token: write` | Runs `verify: "true"` on the roots the default branch has, and writes nothing back |

The workflow runs only on the default branch: a dispatch of another ref fails in its first job. A discover run pushes to `inputs-ref` and never to the default branch: the name is checked twice, and the token that can push goes on disk after infraharvest has run, in a checkout of its own. The selection file lists resource IDs, names and tags, so the branch has them as the repository's other branches do. The files come from a branch, not from the dispatch's inputs, so what a run imports or accepts is in Git's history, where whoever committed it, and when, can be seen; only the files are checked out, into a directory of their own, and nothing else of that branch is run. The inputs reach the shell as variables, never in a script's text, so they can't inject commands. An import's or a drift check's result still arrives as a pull request, which someone reviews and merges: the dispatch changes no code on the default branch itself. One run at a time per repository (`concurrency`): GitHub keeps one running and one pending run, and a newer pending dispatch replaces the older pending one, so a control plane must not dispatch another request while one is pending, or the replaced request's `request-id` never comes back.

## Signed evidence

Off by default. With `sign-evidence: "true"`, after a successful import, the action writes [`infraharvest export`'s documents](../docs/export.md) to `_reports/evidence/` in `out`: the inventory, without tags (the evidence is committed, and the tags of what wasn't imported aren't the repository's), the run, the gate and, from a release that has it, the graph. It writes a `SHA256SUMS` file of them and of the report (`report.json`, `report.md`), and signs it with [cosign](https://docs.sigstore.dev/), keylessly, as the job: `SHA256SUMS.sigstore.json` holds the signature, the certificate GitHub's OIDC identity got for this workflow run, and its entry in Sigstore's public transparency log. The pull request or the commit carries them with the configuration.

Anyone can then check, from the repository alone, which workflow, at which commit, wrote the report and the documents, and that they are as it wrote them:

```sh
cd infrastructure/_reports/evidence
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/<owner>/<repository>/\.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
sha256sum --check SHA256SUMS
```

Narrow the identity to the workflow file and branch that import, such as `--certificate-identity https://github.com/<owner>/<repository>/.github/workflows/infraharvest.yml@refs/heads/main`. The job needs `id-token: write`; without it the step says so and fails. Each run replaces the directory, so it always holds one run's evidence. The signature is recorded in Sigstore's public log, which shows the repository's name and the workflow: don't turn it on for a repository whose name is private.

## Diagram of the imported estate

Off by default. With `diagram: "true"`, after a successful import, the action draws what the import took over as a [Mermaid](https://mermaid.js.org/) diagram: scopes and module calls as boxes, and the references between resources as arrows. It adds the diagram to the job summary and to the body of the pull request, after the reports, where GitHub draws it. With `commit` there is no pull request body, so it is in the job summary only. It needs `import: "true"`, so the action refuses it with `discover`, `verify` and a drift check alone, before it installs anything.

```yaml
      - uses: IgnatG/infraharvest/action@<sha>
        with:
          version: v0.1.0
          provider: aws
          services: vpc,subnet,sg,s3,iam
          regions: eu-west-2
          pull-request: "true"
          sign-evidence: "true"
          diagram: "true"
```

- **What it draws.** The graph document `infraharvest export --schema graph` writes (see [The graph](../docs/export.md#the-graph)), drawn by the [Terragram CLI](https://github.com/hibiscus-ctrl/terragram), release 0.1.0, which the action downloads from that repository's releases, checks against the SHA-256 it pins and whose build provenance it verifies, and runs with Node.js 24 (`actions/setup-node`). The export is local: it makes no cloud call. Nothing is sent anywhere: the diagram is made in the job.
- **What changed.** If the repository already has a graph from before the run, the diagram shows only what the import added, removed or changed, with one reference of context (`terragram graph --base <earlier> --delta`). The earlier graph is the one a run with `sign-evidence` committed, `_reports/evidence/graph.json` in `out`; the action copies it before drift and the import run, so that nothing they write can change it. Without one, such as on the first run or without `sign-evidence`, the diagram has the whole estate.
- **Redaction.** The graph is exported as the [control plane's](#sending-a-run-to-a-control-plane) is, with `--hash-ids` for `control-plane-hash-ids` (and its key) and `--drop-addresses` for `control-plane-drop-addresses`, whether or not `control-plane-url` is set, so the diagram has no ID, name or address that the export would send hashed or leave out. The earlier graph isn't redacted, so with either one the whole estate is drawn, not what changed.
- **Size.** GitHub doesn't draw a Mermaid diagram of more than about 45,000 characters, so a larger one is replaced by a line saying so. A body GitHub would refuse for its length is left without the diagram, which the job summary still has.
- **Failures.** The diagram is an extra: if Node.js or the CLI can't be installed, the export or the drawing fails, or the diagram holds a code fence, the action warns (`::warning::diagram: ...`) and the run goes on, with no diagram. Only a refused input fails the job.

## Drift

Once the roots are applied, people still change things in the cloud. With `drift` set, the action runs [`infraharvest drift`](../docs/drift.md) on the roots in `out` before importing: it refreshes each root once, with read access to its state, and reports which changes the next `terraform apply` would revert. The report, `drift.md`, goes into the job summary, and the `drift-found` output says whether drift is left.

| `drift` | What the action does |
|---|---|
| `off` (default) | No drift check |
| `report` | Finds the changes and writes the decisions file, `drift-decisions` or `drift.yaml` in `out`, with an entry per change to accept or keep, keeping the decisions it has. The code isn't changed, and the decisions file isn't applied |
| `accept` | Writes the cloud's values of the changes that `drift-decisions` (a decisions file you reviewed) or `drift-accept` (rules) accept into the code, once a plan of the edited code shows them taken; the others are kept, and the next apply reverts them. The decisions file is written again with this run's changes |

`drift-accept` takes one rule a line, as drift's `--rules` file does: `accept` or `keep`, then a rule as `--accept` takes it, such as `accept attribute=tags.*` or `keep type=aws_security_group`; a rule alone, such as `all`, is accepted, lines starting with `#` are skipped, and the last rule that matches a change decides (see [Accepting changes](../docs/drift.md#accepting-changes)). `drift-decisions` decides by the file first, and the rules after it. To review changes one by one, run with `drift: report`, commit the decisions file it writes, set each entry's `decision`, then run with `drift: accept` and `drift-decisions` naming that file. To keep the decisions file outside `out`, such as `decisions/drift.yaml`, set `drift-decisions` with `drift: report` too: it is written there, merged with what it has, and never applied in that mode.

Drift runs Terraform's init and plan on the roots in `out`, which install and run the providers and modules they name with the job's cloud credentials: a provider is a program, and the `external` data source runs any program the configuration names. Run drift only on code you trust, such as the default branch on a schedule (see [Run it on code you trust](#run-it-on-code-you-trust)). Running it on a pull request's content, from a fork or from anyone who can open one, runs that content's providers and modules with your credentials; the action, unlike the [MCP server](../docs/mcp.md), doesn't check which providers the roots use, and `--untrusted-roots`, which makes Terraform ignore their `.terraform` directories and filesystem mirrors, still runs the providers they name.

The drift check runs first, on the code the repository has, so that what the import adds isn't taken for drift; then the import adds the new resources (`incremental` must be `true`). One pull request carries both. A run with `import: "false"` only checks drift, and needs no `services`; it still needs `provider`, which chooses the build of the release to install (a release has one build per cloud). On a first run, before `out` exists, there is nothing to check, and the check is skipped.

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
          drift-decisions: infrastructure/drift.yaml
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
| `import` | `true` | Import resources; `false` only checks [drift](#drift), or runs `discover` or `verify` |
| `discover` | `false` | List resources into the selection file, instead of importing, with `provider`, `services`, `regions`, `config` and `args` (see [Discover and verify](#discover-and-verify)). Needs `import: "false"` |
| `verify` | `false` | Check the roots in `out` again, instead of importing, with `config` and `engine`. Needs `import: "false"` |
| `provider` | | The provider command: `aws`, `azure` or `google`. Required when importing or discovering, and with no `binary`, to choose the build of the release to install |
| `services` | | Services to import, comma-separated. Required when importing, unless the `config` file sets them or `--cfn-stack` in `args` leaves them to the stacks |
| `regions` | | Regions, comma-separated |
| `selection` | | A selection file from `infraharvest discover`. Without one, the default selection is imported (`--all`). With `discover`, the file to write, `selection.yaml` without it |
| `engine` | | `terraform` or `tofu`. Unset, the import, drift and verify use what the configuration file sets, or `terraform` |
| `out` | `infrastructure` | Directory for the configuration and the report |
| `config` | | An infraharvest configuration file, for example with the state backend; drift takes its settings `engine`, `terraform-path`, `engine-timeout`, `engine-parallelism` and `catalogue`, and its own `modules`, and verify those and `policy` (`--config`) |
| `incremental` | `true` | Add what is new to the roots `out` already has, after the blocks of the file of their type (`--incremental`), instead of failing on them. Must be `true` with `drift` and an import |
| `args` | | More flags for the import or discover, one a line, each starting with `--` (a bare `--` is refused): for Google Cloud, which names no project by default, `--projects=my-project` (or `--organization`, `--folders`); also `--resource-group`, `--layout` and the like |
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
| `control-plane-url` | | Send the export documents of the import, the drift check, discover or verify to this control plane (see [Sending a run to a control plane](#sending-a-run-to-a-control-plane)). HTTPS only; needs `id-token: write`. Empty, nothing is sent |
| `control-plane-audience` | `infraharvest-api` | With `control-plane-url`, the audience of the OIDC token the session is opened with |
| `control-plane-drop-tags` | `false` | With `control-plane-url`, leave all tags out of the OCSF inventory |
| `control-plane-drop-tag` | | With `control-plane-url`, tag keys to leave out of the OCSF inventory, a line each or comma-separated |
| `control-plane-hash-ids` | `false` | With `control-plane-url`, hash IDs and names in the OCSF inventory and graph, and the addresses in the OCSF inventory, the graph and the drift summary; needs `control-plane-hash-key` or `INFRAHARVEST_EXPORT_KEY` |
| `control-plane-hash-key` | | The key `control-plane-hash-ids` hashes with, from a secret |
| `control-plane-drop-addresses` | `false` | With `control-plane-url`, leave the resource addresses out of the OCSF inventory, the graph and the drift summary |
| `control-plane-request-id` | | With `control-plane-url`, the ID of the control plane's request, which goes into the run document as `request_id` (1 to 128 letters, digits or `. _ : -`) |
| `control-plane-command` | | With `control-plane-url`, `incremental` makes an import's run document say `incremental`, for an incremental import a control plane dispatched; empty, it names what ran |
| `control-plane-optional` | `false` | Warn, rather than fail, when the export or the upload fails |
| `sign-evidence` | `false` | Write the export documents and a signed `SHA256SUMS` of them and the report to `_reports/evidence/` in `out` (see [Signed evidence](#signed-evidence)). Needs `id-token: write` and `import: "true"` |
| `diagram` | `false` | Draw the imported estate as a Mermaid diagram in the job summary and the pull request's body, only what changed when the repository has the graph a run with `sign-evidence` signed (see [Diagram of the imported estate](#diagram-of-the-imported-estate)). Needs `import: "true"`; not with `discover` or `verify` |

## Outputs

| Output | Description |
|---|---|
| `exit-code` | The exit code of infraharvest import, discover or verify, whichever ran: 0 complete, 1 incomplete (for verify, a check failed), 2 couldn't run (for verify, a root couldn't be checked), 3 partial; empty when none ran |
| `selection-file` | Path of the selection file `discover` wrote; empty without `discover` |
| `report` | Path of the import report |
| `drift-found` | `true` when drift is left: a change the next apply would revert, or that can't be assessed. `false` when none is, or without `drift` |
| `drift-report` | Path of the drift report, `drift.md`, if drift ran |
| `drift-exit-code` | infraharvest drift's exit code: 0 no drift left, 4 drift left, 1 an accepted change failed verification, 2 a root couldn't be checked; empty if drift didn't run |
| `pull-request-url` | URL of the pull request, if one was opened |
| `commit-sha` | SHA of the commit pushed with `commit`; empty when the output didn't change, or without `commit` |
| `control-plane-run-id` | The `run_id` of the run document sent to the control plane, once every document of the run was accepted; empty otherwise |
| `evidence-dir` | The directory of the signed evidence, with `sign-evidence`; empty otherwise |

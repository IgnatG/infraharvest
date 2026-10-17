# Troubleshooting

Find the message you see, or the exit code, below. infraharvest logs to stderr, and records what an import did in the `report/` directory of the output (`infraharvest-out/report/` by default): `report.md` for people and `coverage.json` for tools (see [Report](output.md#report)). `infraharvest report [output-directory]` prints it again.

## Exit codes

| Code | What happened | Where to look |
|---|---|---|
| 0 | Everything found was imported | Nothing to do |
| 1 | Something couldn't be imported; the log ends with `output is incomplete; rerun with --allow-partial to accept partial output` | `coverage.json`, below |
| 2 | The import couldn't run: no credentials, no Terraform, a registry out of reach, state that can't be read | The last line of the log |
| 3 | Something couldn't be imported, `--allow-partial` is set (`output is incomplete (--allow-partial is set)`), and the output has the rest | `coverage.json`, below |
| 130 | The import was interrupted with Ctrl-C (SIGINT) | `coverage.json` lists what was done before it stopped |
| 143 | The import was terminated (SIGTERM), as by a CI runner cancelling the job or a timeout | `coverage.json`, as for 130 |

See [Exit codes](output.md#exit-codes). A failed check of the [verification gate](output.md#verification-gate) counts as something that couldn't be imported.

### Reading coverage.json

What went wrong is in three places of `coverage.json`:

- `failures`: one line per thing that failed, such as a service that couldn't be listed (`service s3: ...`), a resource Terraform left out (`<directory>: <address> left out (see rejected.hcl): ...`) or a failed check (`<directory>: G3 plan failed: ...`).
- `directories[].left_out`: the resources of each root that Terraform left out, with its errors. The root's `rejected.hcl` holds their import blocks.
- `directories[].checks`: the verification gate's checks of each root, with `passed` and the `details` of a failure; `directories[].error` says why nothing in a root was imported.

With [jq](https://jqlang.org/):

```sh
jq -r '.failures[]?' infraharvest-out/report/coverage.json
jq -r '.directories[] | .path as $dir | .left_out[]? | "\($dir): \(.address): \(.errors | join("; "))"' infraharvest-out/report/coverage.json
jq -r '.directories[] | .path as $dir | .checks[]? | select(.passed | not) | "\($dir): \(.name): \(.details // [] | join("; "))"' infraharvest-out/report/coverage.json
```

## Access denied

A service fails with the cloud's denial in its message, `service <name>: ...`, and the import exits 1 (or 3 with `--allow-partial`): `AccessDenied` or `UnauthorizedOperation` on AWS, `AuthorizationFailed` on Azure, a `403` with `PERMISSION_DENIED` on Google Cloud. A service denied only a part, such as one Route 53 hosted zone's records or an Azure child resource list, keeps the rest it listed. A resource Terraform couldn't read is left out instead, with Terraform's error, in `rejected.hcl` and `left_out`.

- Run as the principal the [permissions templates](permissions.md) grant: on AWS `ReadOnlyAccess` with the data-read deny, on Azure Reader, on Google Cloud `roles/viewer`, `roles/iam.securityReviewer` and `roles/cloudasset.viewer`.
- On AWS, the templates deny `kms:Decrypt`, so SSM `SecureString` parameters fail to import by design (see [Permissions](permissions.md#aws)).
- `--allow-partial` keeps everything else and exits 3; `--skip=<service>` leaves out a service you can't read.
- If the templates should grant what was denied, please [open an issue](https://github.com/IgnatG/infraharvest/issues) with the resource type and the message.

## A service took too long

`service <name>: listing took longer than --list-timeout 30m0s; its resources may be incomplete` means the service didn't finish listing one account and region, subscription or project in time. Raise `--list-timeout` (`0` for no limit), name fewer `--services`, or on AWS skip services a region doesn't use with [`--resource-explorer`](aws.md#resource-explorer).

## A Terraform command took too long

`terraform plan: timed out after 1h0m0s` (or `terraform init: ...`, `terraform validate: ...`) means one Terraform or OpenTofu command of a root ran longer than `--engine-timeout`, one hour by default. The command is stopped and that root fails; with `--parallel`, the other accounts, subscriptions or projects carry on. A provider retrying an endpoint it can't reach, or a module download that stalls, are the usual causes: run with `TF_LOG=debug` to see which. For a very large root, raise `--engine-timeout` (`0` for no limit). `infraharvest verify` takes the flag too.

## AWS: Resource Explorer isn't set up

With `--resource-explorer`, a log line such as `aws: Resource Explorer can't rule out services in eu-west-2: no index; listing every service` isn't an error: infraharvest lists every service in that region, as it does without the flag, which only takes longer. The reason after the region says what is missing:

| Reason | Fix |
|---|---|
| `no index` | Turn Resource Explorer on in the account, in its console |
| `the index is ...` | Wait until the index is active |
| `the index dates from ..., less than a day ago, ...` | Wait a day: a new index may not have found every resource yet |
| `no default view`, or `no default view, and in <region>, where the index replicates to: ...` | Create a default view in the region, or in the region of the aggregator index |
| `not allowed resource-explorer-2:<action> (...)` | Grant the action; `ReadOnlyAccess` has it |

The view must have no filter and cover its own account only. See [Resource Explorer](aws.md#resource-explorer).

## Google Cloud: the Cloud Asset API is disabled

The `cloud_asset` service fails with `searching project <project>: ...` and a `403` whose reason is `SERVICE_DISABLED`: the Cloud Asset API isn't enabled in the project your credentials' quota goes to, which isn't always the project imported. Enable it there:

```sh
gcloud services enable cloudasset.googleapis.com --project=<project>
```

With user credentials from `gcloud auth application-default login`, the quota project is the one `gcloud auth application-default set-quota-project <project>` sets; with a service account, its own project. A `403` that names `cloudasset.assets.searchAllResources` instead means the principal lacks `roles/cloudasset.viewer`. See [Cloud Asset Inventory](google.md#cloud-asset-inventory) and [Permissions](permissions.md#google-cloud).

A project without the Cloud DNS, Cloud SQL Admin or Cloud Scheduler API enabled has nothing for `dns`, `cloudsql` or `schedulerJobs` to list: that is logged, not a failure.

## Azure: roles

- **Reader** on the subscriptions, or on a management group above them, is what listing needs. Without it, Resource Manager answers `AuthorizationFailed`, and Resource Graph returns only what the identity can read, so an import can find less than there is rather than fail. See [Permissions](permissions.md#azure) and [Resource Graph](azure.md#resource-graph).
- **`--managed-state https://<account>.blob.core.windows.net/...`** reads blobs, which Reader can't: the import stops with `state <source>: ...` and Azure's `AuthorizationPermissionMismatch` (403). Give the identity **Storage Blob Data Reader** on the storage account or the container, or set `ARM_ACCESS_KEY` or `ARM_SAS_TOKEN` (see [Choosing what to import](usage.md#choosing-what-to-import)).
- A storage account's queue and table service settings need the account's keys or a storage data role too, so the default selection leaves them out (see [Permissions](permissions.md#azure)).

## Terraform can't be found or downloaded

The import exits 2 when it has no engine to run:

- `install terraform: ...`: no Terraform 1.5 or later was on `PATH` or in infraharvest's cache, and downloading the latest release from `releases.hashicorp.com` failed, as it does offline or behind a firewall that blocks it. Install Terraform yourself, or pass its path with `--terraform-path`.
- `tofu 1.6.0 or newer not found on PATH; install it, or pass its path`: `--engine=tofu` never downloads OpenTofu. Install it, or pass `--terraform-path`.
- A log line `terraform: not using <path>: ...` says why a binary on `PATH` was passed over, such as a version older than 1.5.

The [container image](install.md#container-image) has pinned Terraform and OpenTofu binaries. A downloaded Terraform is kept in infraharvest's cache directory (below) for the next run.

An import also needs the registry: `look up registry.terraform.io/hashicorp/aws versions: ...` means infraharvest couldn't ask it for the provider's releases, which it pins the newest of, and Terraform downloads the provider from it too, so there is no fully offline import. Behind a proxy, set `HTTPS_PROXY` (and `NO_PROXY`), which infraharvest and Terraform both read; a proxy that re-signs TLS needs its certificate authority trusted (see [Behind a TLS-intercepting proxy](configuration.md#behind-a-tls-intercepting-proxy)). Where the registry's modules can't be reached, `--modules=local` uses generated local modules only (see [Modules](modules.md)).

## Disk space for providers

`terraform init` failing with `no space left on device` means the providers didn't fit: the AWS provider alone is about 700 MB unpacked. infraharvest gives every Terraform it runs one plugin cache, so each provider version is downloaded once, and Terraform links the roots' providers to it where it can. The cache is `infraharvest/plugins` under the user cache directory: `$XDG_CACHE_HOME`, or `~/.cache`, on Linux, `~/Library/Caches` on macOS and `%LocalAppData%` on Windows. It replaces any `TF_PLUGIN_CACHE_DIR` you set. Runs on one machine share it: each `terraform init` holds the lock of `infraharvest/plugins.lock` next to it, so another run waits, logging that it does, rather than install the same provider at the same time. The system releases the lock of a run that is killed.

- Leave a few GB free there, and more for both engines: Terraform's and OpenTofu's registries give the same provider separate copies.
- Each new provider release adds a version; deleting `infraharvest/plugins` frees them all, and the next import downloads what it needs again.
- On Linux, `XDG_CACHE_HOME` moves the cache to a larger disk.

## Emulators

infraharvest can list a local emulator instead of a cloud, as its end-to-end tests do:

- **AWS:** `AWS_ENDPOINT_URL=http://localhost:4566` sends the AWS SDK's calls, and the Terraform provider's, to an emulator such as [Floci](https://github.com/floci-io/floci).
- **Google Cloud:** `INFRAHARVEST_GOOGLE_ENDPOINT=http://localhost:4588` sends the listers' calls to an emulator, without credentials. Point the Terraform provider there too, with its `GOOGLE_*_CUSTOM_ENDPOINT` variables.
- **Azure:** `ARM_ENVIRONMENT=stack` with `ARM_METADATA_HOSTNAME` names an emulator as a custom cloud, which the host must trust the TLS certificate of. An emulator without Resource Graph needs the [services that list through Resource Manager](azure.md#services-to-name) instead of `--services='*'`.

An emulator doesn't enforce permissions or return every attribute a cloud does, so an import that works there can still fail on a real account. The [end-to-end tests' README](https://github.com/IgnatG/infraharvest/blob/main/e2e/README.md) shows each setup and what each emulator lacks.

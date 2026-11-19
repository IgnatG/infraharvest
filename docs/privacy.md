# Privacy

infraharvest collects no telemetry. It has no analytics, crash reports or update checks, and it sends nothing about you, your machine or your estate to its authors or anyone else.

## What it connects to

A run connects only to what it needs, with the credentials you give it:

- **Your clouds' APIs**, to list and read the resources it imports: AWS, Azure Resource Manager and Resource Graph, and Google Cloud's APIs and Cloud Asset Inventory. The [permission templates](permissions.md) grant read access alone.
- **The Terraform or OpenTofu it runs.** `terraform init` downloads providers and modules from their registries (registry.terraform.io, registry.opentofu.org) and from the module sources a root names, as it does for any configuration. infraharvest sets `CHECKPOINT_DISABLE=1` for the Terraform it runs, unless you set it yourself, so Terraform doesn't send HashiCorp its version and platform at each start.
- **HashiCorp's release site** (releases.hashicorp.com), only when a run uses Terraform and finds none new enough on `PATH` or in its cache, to download the latest release, checked against HashiCorp's signed checksums. OpenTofu is never downloaded: it must be installed.
- **The state you point it at**, with `--managed-state` or `infraharvest drift`: an S3, Blob Storage or Cloud Storage backend, or HCP Terraform with the token `terraform login` saved (see [Choosing what to import](usage.md#choosing-what-to-import)).

## What it writes

- **The output directory** (`infraharvest-out` by default): the generated configuration, the report and, under `.infraharvest/`, the inventory of what was listed. The inventory describes the estate as state does, so keep the directory where you would keep state. Secrets never go into the configuration, the report or the logs (see [Output](output.md)).
- **A cache** in your user cache directory (`infraharvest` under it), for the Terraform it downloads and the providers Terraform installs.

## Export

`infraharvest export` is opt-in: nothing runs it for you. It is local: it reads the output directory of an import, makes no network calls and uploads nothing, so whether the file goes anywhere is for you to decide. (The GitHub Action can send the documents for you, but only when you set `control-plane-url`: see below.) It writes one document, to standard output or to `--out-file`:

- **`inventory`**: for each resource its type, ID, account, region, name, tags and who manages it, with counts by type and scope. Tags whose key or value looks like a secret are dropped, and you can drop more with `--drop-tag` and `--drop-tags`, or hash IDs and names with `--hash-ids` under a key only you hold.
- **`run`**: the command, the outcome and, for an import, the exit code, the versions of infraharvest and Terraform or OpenTofu, and the counts.
- **`gate`**: whether each check of the verification gate passed, with reason codes from a fixed list.
- **`drift-summary`**: for each root and environment, how many changes in the cloud drift found, by outcome and resource type, and the addresses of the resources that changed, never what they changed to. `--hash-ids` hashes the addresses, and `--drop-addresses` leaves them out.
- **`ocsf-inventory`**: the inventory's resources as OCSF events, one for each, and, for a resource the import took over, its address in the configuration, the module call it is in and the path of its root, the URL of its root's state if you pass the configuration file with `--config`, and the repository and commit if you name them or GitHub Actions sets them. Each resource also says what the import did with it. `--hash-ids` hashes the addresses, paths and state URLs, and `--drop-addresses` leaves them out.

It holds no attributes, values or messages, and, but for the addresses of the drift summary and the OCSF inventory and the paths of the OCSF inventory's roots, no resource addresses or paths. Each document is checked against its schema before it is written. See [Export](export.md).

## The GitHub Action and the MCP server

The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md) runs infraharvest in your own workflow, with your workflow's credentials, and downloads the release it runs from GitHub. It sends nothing anywhere else by default. Only if you set `control-plane-url` does it send anything: then it runs `infraharvest export` and sends exactly the three [export](export.md) documents, inventory, run and gate, with the redaction you chose, to that URL (HTTPS only), authenticated with the job's GitHub Actions OIDC token, to the endpoints under `/api/v1/ingest/`, and nothing else. See [Sending a run to a control plane](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane). The [MCP server](mcp.md) runs on your machine and talks to the AI host that started it over standard input and output. What the host does with the results, such as sending them to a model, is up to the host.

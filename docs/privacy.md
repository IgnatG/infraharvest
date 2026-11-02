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

## The GitHub Action and the MCP server

The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md) runs infraharvest in your own workflow, with your workflow's credentials, and downloads the release it runs from GitHub. The [MCP server](mcp.md) runs on your machine and talks to the AI host that started it over standard input and output. What the host does with the results, such as sending them to a model, is up to the host.

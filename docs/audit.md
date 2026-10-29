# Audit

What in an account isn't managed by Terraform yet? infraharvest can answer that without importing anything: it lists the resources, leaves out what a Terraform state, another tool or the cloud itself manages, and gives you the rest. Nothing is applied, no state is written, and nothing needs to be committed: the results are files in a directory you delete afterwards.

There are two ways, and you can run both:

| Way | What you get | What it takes |
|---|---|---|
| `discover` | Every resource, with whether something manages it and what, in a selection file; counts in the log, and by type, by account and region and by tag with `--format json` | Listing only: seconds to minutes per region, no Terraform |
| `import` into a scratch `--out` | The same counts in `report.md` and `report.json`, with the owner tags discover can't read and the types Terraform can't import | A Terraform plan per root: minutes per region |

## When to use it

- On first contact with an account, subscription or project: to see how much of it is under infrastructure as code, and what is not.
- Before committing to an import: to size it, and to find which owners or teams still have resources outside Terraform (`--report-tags`).
- From time to time, to find what was created by hand since.

## What it needs

- infraharvest ([Install](install.md)).
- Credentials that can read the resources. The [permission templates](permissions.md) grant read-only access on each cloud; the audit needs nothing more for the listing.
- For `--managed-state`, read access to the state it reads. The read-only templates don't grant it: the AWS role denies `s3:GetObject` and `kms:Decrypt`, Azure's Reader has no data actions, and Google Cloud's template denies reading and listing objects (`storage.objects.get`, `storage.objects.list`) with an IAM deny policy (`deny_data_reads`, on by default). The drift templates grant read access to the state alone (see [Permissions](permissions.md#drift-reading-the-state)). Blob Storage is read as the azurerm backend reads it, and `tfc://` with the token `terraform login` saved (see [Choosing what to import](usage.md#choosing-what-to-import)). A state that can't be read fails the command before anything is listed.
- For the full report only, Terraform 1.5 or later, or OpenTofu 1.6 or later, which infraharvest downloads if there is none on `PATH`.

Run it in an empty directory: `discover` writes the selection file into the current directory, and saves what it listed under `<out>/.infraharvest` (`infrastructure` by default). That saved list holds every resource's attributes, so it is as sensitive as state: `.infraharvest` is created private to your user (mode 0700, or on Windows an access list that grants your user, SYSTEM and Administrators only), but delete the directory once you are done.

## Quick audit: discover

`discover` lists every service (`--services='*'`) into a selection file, and marks each resource included or not, with the reason. Point `--managed-state` at the state your Terraform roots keep, so that what they manage is marked too. It takes state files, directories of them, `s3://`, `gs://` and Blob Storage prefixes, HCP Terraform workspaces (`tfc://<organization>/<prefix>*`), and several of them, separated by commas.

=== "AWS"

    ```sh
    cd "$(mktemp -d)"
    export AWS_PROFILE=prod
    infraharvest discover aws --services='*' --regions=eu-west-2,us-east-1 \
      --managed-state='s3://my-terraform-state/prod?region=eu-west-2' \
      --selection=audit.yaml --pick=false
    ```

=== "Azure"

    ```sh
    cd "$(mktemp -d)"
    az login
    export ARM_SUBSCRIPTION_ID=<subscription ID>
    infraharvest discover azure --services='*' \
      --managed-state=https://<storage account>.blob.core.windows.net/tfstate/ \
      --selection=audit.yaml --pick=false
    ```

=== "Google Cloud"

    ```sh
    cd "$(mktemp -d)"
    gcloud auth application-default login
    infraharvest discover google --services='*' --projects=<project ID> --regions=global,europe-west2 \
      --managed-state=gs://my-terraform-state/prod \
      --selection=audit.yaml --pick=false
    ```

Without `--managed-state`, the audit still marks what CloudFormation stacks and owner tags say another tool manages, and what the cloud manages itself. Several accounts, subscriptions or projects work as for an import: `--accounts` or `--organization` on AWS, `--subscriptions` or `--management-group` on Azure, `--organization` or `--folders` on Google Cloud (see [AWS](aws.md), [Azure](azure.md) and [Google Cloud](google.md)).

The log ends with how much it listed, by what manages it: `listed 412 resources into audit.yaml: 315 not managed, 97 excluded by default (40 in a Terraform state, 12 by Terraform tags, 20 by CloudFormation, 0 by other tools, 25 by the cloud or other defaults)`. With `--report-tags owner,team`, a line for each tag key follows, such as `by team: (not tagged) 120 of 150 not managed, web 30 of 60 not managed`. `discover` exits 0 when everything listed, and 1 when a service, an account or the tags failed to list, which the log names: the audit is then incomplete for them.

With `--format json`, `discover` prints the same counts on stdout as one JSON document, instead of opening the picker: in all, by type (`types`), by account and region (`scopes`) and, with `--report-tags`, by the value of each tag key (`tags`), each with `listed`, `included` (not managed), `excluded`, and the excluded by what manages them: `terraform_state`, `terraform_tag`, `cloudformation`, `other_tool` and `cloud` (what the cloud manages itself, or the default selection leaves out for another reason). `failures` lists what couldn't be listed. The counts are of what this run listed, as the default selection decides it, whatever the selection file's own decisions:

```sh
infraharvest discover aws --services='*' --regions=eu-west-2 --managed-state=./states \
  --selection=audit.yaml --report-tags=team --format=json > audit.json
jq -r '.types[] | select(.included > 0) | [.type, .included] | @tsv' audit.json
```

Run again on an existing file, `discover` brings the decisions it made itself up to date with what manages each resource now. An entry it included is excluded once a Terraform state, CloudFormation or another tool manages the resource. One it excluded takes its new reason, or, once nothing manages it, is decided again by the file's rules and defaults. Either change is marked `new: true` for review. A reason is brought up to date only when the run worked it out again in full: one from a state only with `--managed-state`, one from tags not with `--include-owned`, and none after a listing that failed in part. Your own decisions stay as they are: an entry with a `note`, one you included although it was excluded with a reason, one you excluded, and one a rule of the file or its defaults decided.

### Reading the selection file

Each entry has the resource's type, ID, name, account and region (`scope`), the service that listed it, its tags where discover could read them, and `include`:

| Entry | Means |
|---|---|
| `include: true` | Nothing infraharvest can tell manages it: no state `--managed-state` read has it, no tag names an owner, no CloudFormation stack lists it, and it isn't something the cloud manages itself. This is what isn't under infrastructure as code |
| `reason: already managed by Terraform (<state>)` | A state `--managed-state` read has it; the reason names the state file or object |
| `reason: already managed by Terraform (... per its tag ...)` | Its tags say another Terraform or OpenTofu state manages it, such as `ManagedBy = Terraform` or Google's `goog-terraform-provisioned` label |
| `reason: managed by CloudFormation stack <stack>` | On AWS, a CloudFormation stack manages it, CDK apps and nested stacks included |
| `reason: managed by <tool> (per its tag ...)` | Its tags say another tool manages it, such as Pulumi or Crossplane |
| Any other `reason` | The default selection leaves it out for another reason, mostly because the cloud creates and manages it itself, such as a default VPC or a service-linked role (see each cloud's page: [AWS](aws.md#what-the-default-selection-leaves-out), [Azure](azure.md#resource-graph), [Google Cloud](google.md#cloud-asset-inventory)) |

`infraharvest pick --selection audit.yaml` shows the file as a tree, by account and region, then type, and searches by type, ID, name, reason or tag: search `managed by` for what is managed elsewhere. Leave it with `q`, which changes nothing.

With [yq](https://github.com/mikefarah/yq):

```sh
# what isn't under infrastructure as code, by type
yq '.resources[] | select(.include) | .type' audit.yaml | sort | uniq -c | sort -rn
# the same, one resource per line
yq '.resources[] | select(.include) | .scope + " " + .type + " " + .id' audit.yaml
# what Terraform manages
yq '.resources[] | select((.reason // "") | test("^already managed by Terraform")) | .type + " " + .id' audit.yaml
# what another tool manages, and which
yq '.resources[] | select((.reason // "") | test("^managed by ")) | .type + " " + .id + ": " + .reason' audit.yaml
# what isn't under infrastructure as code and has no owner tag
yq '.resources[] | select(.include and .tags.owner == null) | .type + " " + .id' audit.yaml
```

## Full report: import into a scratch directory

An import with `--all` brings in everything the default selection includes, which, with `--managed-state`, is what nothing manages. Into a scratch `--out`, it is an audit: it writes configuration you don't keep, never applies it, writes no state, and never reaches a backend. It runs `terraform plan` on every root it generates, so it takes longer than `discover`, and Terraform's provider reads each resource.

```sh
infraharvest import aws --services='*' --regions=eu-west-2,us-east-1 --all \
  --managed-state='s3://my-terraform-state/prod?region=eu-west-2' \
  --out=audit-out --report-tags=owner,team --modules=none
```

`--modules=none` skips trying clusters of resources as module calls, which an audit doesn't need. Right after the quick audit, add `--reuse-inventory` to import from what `discover` listed into the same `--out`, with the same services and filters, instead of listing again. The same goes for Azure and Google Cloud, with their own flags.

The import exits 1 when a resource couldn't be imported or a check failed, and 3 with `--allow-partial`; the report is written either way, and the counts of what is managed don't depend on it.

### Reading the report

`audit-out/_reports/report.md` (`infraharvest report audit-out` prints it again) has:

- A table of totals: discovered, imported, excluded, left out, not importable (types Terraform can't import) and failed.
- **Managed and unmanaged**: for each type, and for each account and region when there are several, how much was discovered, how much Terraform manages (by the state, by tags, or, with `--incremental`, in a root of the output already), how much another tool manages, how much the cloud manages itself (or the default selection leaves out for another reason, such as a VPC another account shares), and how much none of them does. It is there whenever something was discovered, also when nothing is managed.
- **By tag**, with `--report-tags`: the same for each value of each tag key, `(not tagged)` included, with what was imported.
- **Findings by resource**: what the gate's scanners, and your policies with `--policy`, found about each imported resource, to review before you apply (see [Findings by resource](output.md#findings-by-resource)).
- **Excluded**: every resource the selection left out, with its reason, as in the selection file.
- **Failures**: services, accounts or projects that couldn't be listed. The audit is incomplete for them.

`Not managed` is what was discovered less what Terraform, other tools and the cloud manage. It still counts what you left out yourself, by the selection file, and types Terraform can't import. What was imported, and what was left out after Terraform rejected it, is what is unmanaged and importable.

`report.json` holds the same, for tools, as `--format json` prints it on stdout (see [Report](output.md#report)). `managed`, `other_tool` and `cloud` are left out where they are 0. Each of `excluded` has an `owner`, what manages it: `terraform_state`, `terraform_tag`, `terraform_root` (a root of the output has it), `cloudformation`, `other_tool`, `cloud`, or `selection` (you left it out). With [jq](https://jqlang.org/):

```sh
# totals: discovered, imported, excluded, managed, other_tool, cloud ...
jq '.totals' audit-out/_reports/report.json
# what isn't managed, by type
jq -r '.types[] | [.type, .discovered - (.managed // 0) - (.other_tool // 0) - (.cloud // 0)] | @tsv' audit-out/_reports/report.json
# the same by account and region
jq -r '.scopes[]? | [.scope, .discovered - (.managed // 0) - (.other_tool // 0) - (.cloud // 0)] | @tsv' audit-out/_reports/report.json
# what is unmanaged and importable, one resource per line
jq -r '.directories[] | (.imported[]?, .left_out[]?) | "\(.address) \(.id)"' audit-out/_reports/report.json
# what other tools manage, with the tool
jq -r '.excluded[]? | select(.owner == "cloudformation" or .owner == "other_tool") | "\(.type) \(.id): \(.reason)"' audit-out/_reports/report.json
```

`not_in_cloud` stays empty in a scratch directory: it lists what roots an earlier import generated have that the cloud no longer has (see [Choosing what to import](usage.md#choosing-what-to-import)).

## Limits

- Only what infraharvest lists is audited: the services it has (`infraharvest services <provider>`, and the [services reference](reference/services.md)), and the types its catalogue maps. Some types aren't listed at all, such as Google Cloud's bucket IAM policies and notifications ([Types that aren't listed](google.md#types-that-arent-listed)) or Azure's storage blobs. Resources inside a resource, such as S3 objects, table rows or secrets' values, are never read.
- `discover` lists only the types Terraform can import; the import's report counts the others as not importable.
- `discover` reads tags only for some types: on AWS, those the Resource Groups Tagging API returns, which leaves out IAM roles and users. A resource whose owner tag it can't read shows as `include: true`. The import reads the tags in the configuration Terraform generates, and counts such resources as managed.
- `--managed-state` knows only the states it reads. Resources in a state it isn't pointed at, such as another bucket or account, a laptop's local state or a workspace the pattern misses, show as not managed.
- A state's resource matches a listed one by type, or another name of its type (`aws_alb` for `aws_lb`), and by ID. What Terraform manages inline in another resource's block, or as a type of another shape, isn't matched: security group rules written inline in an `aws_security_group`, or as `aws_security_group_rule`, show as not managed, as infraharvest lists them as `aws_vpc_security_group_ingress_rule` and `aws_vpc_security_group_egress_rule` by their `sgr-` IDs.
- Tags that name an owner say what was true when they were set. A resource tagged `ManagedBy = Terraform` whose state is lost still counts as managed: `--include-owned` imports it.
- Inventory APIs can lag: Cloud Asset Inventory by a few minutes, by hours for Cloud Functions, and AWS Resource Explorer by hours, if you use `--resource-explorer`, which is off by default.

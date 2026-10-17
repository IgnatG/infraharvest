# Usage

```text
infraharvest discover <provider> [flags]     list resources into a selection file to review
infraharvest pick --selection <file>         review a selection file in the terminal
infraharvest import <provider> [flags]       generate configuration for what a selection includes
infraharvest services <provider>             list the provider's services
infraharvest verify [output-directory]       run the verification gate on generated roots again
infraharvest report [output-directory]       print an import's report again
infraharvest bootstrap --config <file>       write a root that creates the state storage
infraharvest mcp                             serve infraharvest to AI agents over MCP
infraharvest version
```

Every flag of every command is in the [command-line reference](reference/cli.md). Providers add flags of their own, such as `--profile` and `--regions` for AWS or `--projects` for Google Cloud: see [AWS](aws.md), [Azure](azure.md) and [Google Cloud](google.md).

## Choosing what to import

An import must say what it imports, so a whole account never comes under Terraform by accident:

```sh
infraharvest discover aws --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
# review selection.yaml: set include to false to leave a resource out
infraharvest import aws --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
```

`discover` lists every resource it finds into the selection file, each marked included or not. By default it leaves out resources AWS creates and manages itself, with the reason: the default VPC with its subnets, route tables and internet gateway, default security groups and network ACLs, the security groups EKS creates for clusters, service-linked roles, and the log groups Lambda creates. It also leaves out what CloudFormation stacks manage, including CDK apps, with the stack named as the reason: importing those would give them two owners. Global services such as IAM are checked against the stacks of every enabled region. Resources whose tags or labels say another Terraform state or tool manages them are left out the same way, with the tag as the reason: a `ManagedBy`, `managed-by` or `managed_by` tag (any case) naming Terraform, OpenTofu, Pulumi, CloudFormation, the CDK or Crossplane, a `Terraform = true` tag, CloudFormation's `aws:cloudformation:stack-name` tag, or the `goog-terraform-provisioned` label the Google provider gives what Terraform creates. Discover reads tags only for some types (see below), so the import also looks at the tags in the configuration Terraform generates, and leaves out the resources they mark, such as IAM roles, with the reason in the report. `--include-owned` imports them all the same, for example to move them from a state that is lost. You can include any of them by setting `include: true`. Rules in the file (`exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }`) decide resources it doesn't list, such as ones created since. Each entry records the resource's tags (labels on Google Cloud) where discover can read them, and rules can match tags too: `exclude: { tags: { env: dev } }` leaves out what is tagged `env=dev`, and `{ owner: "" }` matches resources without an `owner` tag. On AWS, discover reads tags from the Resource Groups Tagging API of each region it lists (us-east-1 for the global services). It finds tags only for resources whose ID is an ARN it lists, or whose ID or name is in the ARN of a common taggable type, so IAM roles and users have none: the API doesn't list them. A bucket's configuration resources (versioning, encryption, ...) follow the bucket.

In a terminal, `discover` then opens the picker on the file (`--pick=false` skips it); `infraharvest pick --selection selection.yaml` opens it again later. The picker shows the resources as a tree, by account and region, then type. You can include or exclude one resource, or a whole type or account at once, and search by type, ID, name, note, reason or tag. `team=web` in the search shows only resources tagged `team` with a value containing `web`, and `team=` those without a `team` tag; several terms must all match. You can also show only what is new since the last `discover`. A summary keeps count of what is selected, how many roots it makes, and how many resources may become calls of curated modules. `s` saves the file and `q` leaves it as it was. Deciding on a resource marked `new` removes the mark. `discover` records each resource's account and region in the file (`scope: aws/123456789012/eu-west-2`), and the service that listed it (`service: vpc`).

`--all` imports everything the default selection includes, without a file. The report lists what was excluded and why.

Running `discover` again updates the selection file. Entries keep their decisions and notes, and resources that no longer exist are dropped, but only where this run listed: entries of accounts, regions, projects or services it didn't list stay as they were, and so do all the entries it didn't find when it listed with `--id`, `--tag` or `--where`, or when the listing failed in part, as for an account whose credentials expired. So `discover aws --regions eu-west-1` refreshes that region and leaves the rest of the file alone. New resources are added with `new: true`, decided by the file's rules and defaults. Review those, then remove the mark.

`discover` also saves what it listed under `<out>/.infraharvest`. `import --selection selection.yaml --reuse-inventory` imports from that saved list instead of listing the cloud again, as long as it covers the same services, region and account. On a large estate, that saves the listing time a second time. Child resources, such as an S3 bucket's configuration, are still read at import.

Resources that Terraform already manages can be left out too. `--managed-state` reads existing state, in the format Terraform 0.12 and later and OpenTofu write, and excludes every resource it finds there, with the state file as the reason. State from Terraform 0.11 or earlier (format version 3) is refused: upgrade it with Terraform 0.12 or later first. The flag accepts state files, directories of them, `s3://bucket/prefix?region=...`, `gs://bucket/prefix`, or `https://<account>.blob.core.windows.net/<container>/prefix` (`blob.core.usgovcloudapi.net` and `blob.core.chinacloudapi.cn` in Azure's US Government and China clouds; any other https host is refused, so the Azure credentials go nowhere else), reading every `.tfstate` object under the prefix with the cloud's default credentials (in Blob Storage also the `<key>env:<workspace>` blobs where the azurerm backend keeps the state of workspaces other than `default`, and in S3 the `env:/<workspace>/<key>` objects where the S3 backend keeps them, at the bucket's root, for the keys under the prefix: `&workspace_key_prefix=...` names another prefix than `env:`). Resource IDs match the way Terraform reads them: Azure IDs whatever their case, and a type by its other names, such as `aws_alb` for `aws_lb` or `azurerm_virtual_machine` for `azurerm_linux_virtual_machine`. S3 state is read with `--profile`, unless the source names another with `&profile=...`. Blob Storage is read as the azurerm backend reads it: with `ARM_ACCESS_KEY`, `ARM_SAS_TOKEN` or a service principal's `ARM_CLIENT_*` variables, and otherwise Azure's default credentials, such as the Azure CLI login, which need a data role such as Storage Blob Data Reader. `tfc://<organization>/<workspace>` reads the current state of an HCP Terraform workspace, and `tfc://<organization>/<prefix>*` of every workspace whose name starts with the prefix (`?host=` names a Terraform Enterprise host), with the token `terraform login` saved or `TF_TOKEN_<host>`. `--managed-state=backend` reads the S3, GCS or azurerm backend from the configuration file, so an import run again only picks up what is new. The report then shows how much of what was discovered Terraform manages, how much another tool such as CloudFormation manages, and how much neither does. It breaks this down by type, and by account and region when the import covers more than one (`scopes` in `coverage.json`). State can hold secrets: infraharvest reads it only when asked, keeps only resource types and IDs, and needs read access to the state for it. A source that names no bucket, container, organization or workspace, or a local path that isn't there, fails the command before anything is listed, as does a `--selection` file or `--catalogue` directory that doesn't load. So does a source that holds no state once read: a directory without `.tfstate` files, a prefix without `.tfstate` objects, or a workspace pattern that matches no workspace.

When part of an estate is already under Terraform, whether written by hand or imported earlier with another tool, point `--managed-state` at that state before importing the rest. What it manages is then left out, so nothing is imported twice, and the new roots hold only what no state has yet:

```sh
infraharvest import aws --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml \
  --managed-state=./existing-roots
```

A run that fails part way, such as on one region, can be run again with `--resume`: roots generated from the same resources and options since then are kept as they are, with their results. The others are generated again from scratch. Checkpoints go into `<out>/.infraharvest`, which `.gitignore` excludes. A root directory that already holds `.tf` files no checkpoint says an import wrote, such as an unrelated root, is refused rather than overwritten, and `--resume` only removes files from roots a checkpoint says an import wrote.

One run writes to an output directory at a time. `discover`, `import` and `verify` hold a lock on `<out>/.infraharvest/lock` while they run, which records the holder's process ID, host and start time. A second run into the same `--out`, from another terminal or an MCP tool call, fails at once, before it lists anything, naming the run that holds it. The lock is the operating system's own, so a run that is killed releases it with its process, and the next run takes it over. `--force-unlock` writes there anyway, for a run that is stuck, or a file system that can't lock: only pass it when nothing else is writing there.

Once the roots are in use, `--incremental` adds what is new to them instead, without changing what they have. Each new resource goes after the blocks of the file of its type, such as `network.tf` for a subnet, which keeps the blocks it has as they are, or into a new file of that name; one that has no such file goes into a file of its own, `generated_2.tf`, then `generated_3.tf` and so on. Its import blocks, variables, locals and data sources are added after the root's own, and nothing new takes a name the root uses. New resources refer to the resources the root has, such as `vpc_id = aws_vpc.main.id`. They don't repeat the tags the root's provider already applies, and those without one of them use the provider's second configuration, without `default_tags`, which the root gets if it has none. A root's resources are found from its import blocks and from the checkpoint of the import that generated it; for roots applied since, with their import blocks deleted, add `--managed-state` too. A resource another root of the output imports, by its checkpoint, is left out too, as `already in another root of the output`, with that root's path: each resource goes into one root only, so a root an earlier import generated keeps what it has even where an import now puts it in another root, such as a Google Cloud subnetwork in its region's root, which imports now put in the global root with its network. To move such resources, generate the roots again without `--incremental`. A root whose import didn't finish, with no checkpoint of a result or, without a checkpoint, a `generated.tf` but no README, isn't added to: it is reported as failed, and `--resume` generates it again first. The new resources are generated and planned in a directory of their own first, because planning the root itself would need its state. In the root, the other checks run on the files as merged. Clusters of new resources can become curated module calls, but they aren't moved into generated local modules.

```sh
infraharvest discover aws --regions=eu-west-2 --selection=selection.yaml   # marks what is new
infraharvest import aws --regions=eu-west-2 --selection=selection.yaml --incremental --managed-state=backend
```

## Services

A provider lists its resources service by service, and `--services` (`-s`) says which services: `--services=vpc,subnet,sg,s3`. Every `import` and `discover` needs it, on the command line or in the [configuration file](configuration.md). `--services='*'` stands for all of the provider's services except the opt-in ones (quote the `*` so the shell leaves it alone). `--skip` then leaves services out, whether they were named one by one or through `*`:

```sh
infraharvest discover aws --services='*' --skip=iam,organization --regions=eu-west-2
```

`infraharvest services <provider>` prints a provider's services, and the [services reference](reference/services.md) gives the Terraform types each one lists. A service name the provider doesn't have fails like a service that can't be listed. Azure's opt-in services list what `resource_graph` lists already, so they are listed only when `--services` names them: each lists one kind of resource through Resource Manager, such as `virtual_network` (see [Azure](azure.md)). AWS's opt-in services are those of services at end of support, WAF Classic and MediaStore (see [AWS](aws.md#services-at-end-of-life)).

Services are listed several at a time (`--list-parallel`, 4 by default). One that fails, or takes longer than `--list-timeout` (30 minutes by default), is left out of the output and reported, and the run exits with 1, or with 3 under `--allow-partial` (see [Exit codes](output.md#exit-codes)). One that listed but failed on a part, such as `route53` when the records of a hosted zone fail to list, keeps what it listed, and is reported and sets the exit code the same way. So do tags that fail to read, as when `tag:GetResources` is denied: selection rules by tag, owner detection, `{env}` layouts and the report's counts by tag would miss them, so the run goes on with what it has but doesn't pass as complete.

## Running in parallel

Two flags set how much of an import runs at once:

| Flag | Default | What runs at once |
|---|---|---|
| `--list-parallel` | `4` | the services listed for one account and region, subscription or project |
| `--parallel` | `1` | the accounts, subscriptions or projects, and their regions, imported together; the limit counts all of them, so `--parallel 4` never runs more than four at once |

`--parallel` stays at 1 by default because each account, subscription, project or region it runs at once has a Terraform of its own, with its provider loaded, so memory use grows with it. Listing is quick next to generating: in an AWS region, listing takes seconds and generating its roots takes minutes. So the time `--parallel` saves is close to the generate time of every region or account beyond the first, which makes it worth raising for an import of many regions or accounts:

```sh
infraharvest import aws --services='*' --regions=eu-west-1,eu-west-2,us-east-1 --all --parallel 3
```

Start at 2 to 4 and watch the machine's memory, more on a large machine. The global services and those only in us-east-1 (AWS) and a project's first region (Google) are still imported first, on their own, as the other regions refer to them. The output is the same whichever `--parallel` is set, and the report keeps the order of `--regions`.

Lower `--list-parallel` when an API throttles the listing, as the requests of every service listed at once count towards the same limits.

## Filtering

`--id`, `--tag` and `--where` keep only some of what the services list. Each can be given several times, and the selection file and the picker still decide among what passes. Values are separated by commas.

**IDs.** `--id <type>=<id>[,<id>...]` keeps only the resources of that type with one of those IDs, as the lister recorded them: a VPC's ID on AWS, a bucket's name, a full resource ID on Azure. The type is the Terraform type without the provider's prefix: `vpc` for `aws_vpc`, `virtual_network` for `azurerm_virtual_network`, `storage_bucket` for `google_storage_bucket`.

```sh
infraharvest import aws --services=vpc,subnet --regions=eu-west-2 --all --id=vpc=vpc-0a1b2c3d,vpc-4e5f6a7b
infraharvest import google --services=cloud_asset --projects=my-project --all --id=storage_bucket=my-logs
infraharvest import azure --services=virtual_network --all \
  --id=virtual_network=/subscriptions/<subscription>/resourceGroups/web/providers/Microsoft.Network/virtualNetworks/web
```

An `--id` applies to its own type only: the first example keeps every subnet, and an S3 bucket's policy (`aws_s3_bucket_policy`) is a type of its own, which `--id=s3_bucket=my-logs` doesn't narrow. Give an `--id` for each type to narrow, as in `--id=s3_bucket=my-logs --id=s3_bucket_policy=my-logs`. Several `--id` on one type must all match, so to keep several IDs of one type, list them in one `--id`.

**Tags.** To choose resources by tag on every service, use the selection file's rules. Rules decide each resource discover adds to the file, so write them before the first `discover`, into the file it will write:

```yaml
version: 1
defaults:
  include: false
rules:
  - include: { tags: { Environment: prod } }
  - exclude: { type: aws_cloudwatch_log_group, tags: { owner: "" } }
```

The last rule that matches a resource decides it, else the defaults: here everything tagged `Environment=prod` is included, but for its log groups without an `owner` tag. In a file discover already wrote, each listed resource keeps its decision, and rules decide only the resources it doesn't list yet, such as ones created since. To change those decisions, set `include` on the entries, or find them by tag in the picker. Rules match the tags discover reads, so resources it reads no tags for, such as IAM roles and users on AWS, match only `""` (see [Choosing what to import](#choosing-what-to-import)).

**Filtering while listing.** `--tag <key>=<value>[,<value>...]` keeps the resources whose tag has one of the values, and `--tag <key>` those that have the tag at all. `--where <path>=<value>[,<value>...]` does the same for the attribute at a dotted path, as the lister records it, such as `tags.Environment` (what `--tag Environment` filters on). They apply to every type, and save the requests of what they leave out, but only the listers that support them apply them, as they list; the others ignore them, so the import logs a warning for each. Only these AWS listers support them, on tags:

| Service | Example |
|---|---|
| `ec2_instance` | `--tag Environment=prod` |
| `ebs` | `--tag team=web,data` |
| `api_gateway` | `--tag owner=payments` |

A REST API left out by a filter takes its stages, resources, methods and the rest with it. On every other service, filter by tag with the selection file's rules, as above.

## Day to day

Once the roots are applied (see [Applying the roots](output.md#applying-the-roots)), an import is something you run again:

| To | Use |
|---|---|
| Add new resources to the roots | `discover` again, review what it marks `new`, then `import` with `--selection` and `--incremental`; once a root's `imports.tf` is deleted, add `--managed-state` (see [Choosing what to import](#choosing-what-to-import)) |
| Finish a run that failed part way | The same command with `--resume`: roots generated from the same resources and options are kept, the others generated again |
| Import what `discover` just listed, without listing again | `--reuse-inventory`, with the same `--out`, services and filters |
| Check roots after editing them | `infraharvest verify [output-directory]`, before deleting `imports.tf` (see [Verification gate](output.md#verification-gate)) |
| See what changed in the cloud since applying | `terraform plan` in the root |
| Print the report again | `infraharvest report [output-directory]`, with `--format json` for JSON |
| Use the result in a script | `--format json` prints the report on stdout, and logs go to stderr; the exit code says whether the import is complete (see [Exit codes](output.md#exit-codes)) |
| Keep what imported when something fails | `--allow-partial`: what failed is left out and reported, and the run exits 3 instead of 1 |
| Import faster | `--parallel` for accounts, subscriptions, projects and regions, `--list-parallel` for services (see [Running in parallel](#running-in-parallel)) |
| Split roots by environment | `--env-tags=Environment`, or `{env}` in `--layout` (see [Layout](output.md#layout)) |
| Choose what becomes a module call | `--modules`: `registry` (the default), `latest-untested`, `local` or `none`; your own modules go in the [configuration file](configuration.md) (see [Modules](modules.md)) |
| Import less | `--services` and `--skip`, then `--id`, `--tag` and `--where` (see [Filtering](#filtering)) |
| Run on a schedule | The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md), which can open a pull request with the result |
| Let an AI agent prepare a selection | `infraharvest mcp` (see [AI agents](mcp.md)) |

## Options that go together

| Options | Rule |
|---|---|
| `--selection`, `--all` | `import` needs exactly one of them |
| `discover` with `--all` | Refused: `discover` lists everything, and its `--selection` names the file it writes (default `selection.yaml`) |
| `--services`, `--skip` | `--services` is required, on the command line or in the configuration file; `--skip` leaves services out of it. Both must name services the provider has, or `*` |
| `--incremental` with `--resume` | A root whose import didn't finish is generated again, then added to; with `--incremental` alone, such a root is reported as failed |
| `--incremental` with `--managed-state` | A root's import blocks, or the checkpoint in `<out>/.infraharvest` of the import that generated it, say what it has; with neither, `--managed-state` is required |
| `--managed-state=backend` | Needs a configuration file with an `s3`, `gcs` or `azurerm` backend |
| `--reuse-inventory` | For `import`: takes what `discover` saved into the same `--out`, if it lists the services with the same `--id`, `--tag` and `--where`, for the same account and region; otherwise it lists again |
| `--layout` | Starts with `{out}/`, and uses only `{out}`, `{provider}`, `{account}`, `{region}`, `{service}` and `{env}` |
| `--layout` with several scopes | Must contain `{account}`: on AWS with `--accounts` or `--organization`, on Azure with more than one subscription, on Google Cloud with more than one project (or `{provider}/{service}` there) |
| `--env-tags` with `--layout` | A layout of your own needs `{env}`; `{env}` without `--env-tags` reads `Environment`, `environment`, `Env` or `env` |
| AWS `--accounts`, `--organization` | One or the other; with either, `--assume-role` must contain `{account}` |
| Azure `--subscriptions`, `--management-group` | Combine, and without either the import covers `ARM_SUBSCRIPTION_ID`; `--resource-group` narrows each subscription to that group |
| Google Cloud `--projects`, `--organization`, `--folders` | Combine; at least one is required |
| `--allow-partial` | Turns exit code 1 into 3; an import that can't run still exits 2 |

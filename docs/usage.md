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

Resources that Terraform already manages can be left out too. `--managed-state` reads existing state, in the format Terraform 0.12 and later and OpenTofu write, and excludes every resource it finds there, with the state file as the reason. State from Terraform 0.11 or earlier (format version 3) is refused: upgrade it with Terraform 0.12 or later first. The flag accepts state files, directories of them, `s3://bucket/prefix?region=...`, `gs://bucket/prefix`, or `https://<account>.blob.core.windows.net/<container>/prefix` (`blob.core.usgovcloudapi.net` and `blob.core.chinacloudapi.cn` in Azure's US Government and China clouds; any other https host is refused, so the Azure credentials go nowhere else), reading every `.tfstate` object under the prefix with the cloud's default credentials (in Blob Storage also the `<key>env:<workspace>` blobs where the azurerm backend keeps the state of workspaces other than `default`). Resource IDs match the way Terraform reads them: Azure IDs whatever their case, and a type by its other names, such as `aws_alb` for `aws_lb` or `azurerm_virtual_machine` for `azurerm_linux_virtual_machine`. S3 state is read with `--profile`, unless the source names another with `&profile=...`. Blob Storage is read as the azurerm backend reads it: with `ARM_ACCESS_KEY`, `ARM_SAS_TOKEN` or a service principal's `ARM_CLIENT_*` variables, and otherwise Azure's default credentials, such as the Azure CLI login, which need a data role such as Storage Blob Data Reader. `tfc://<organization>/<workspace>` reads the current state of an HCP Terraform workspace, and `tfc://<organization>/<prefix>*` of every workspace whose name starts with the prefix (`?host=` names a Terraform Enterprise host), with the token `terraform login` saved or `TF_TOKEN_<host>`. `--managed-state=backend` reads the S3, GCS or azurerm backend from the configuration file, so an import run again only picks up what is new. The report then shows how much of what was discovered Terraform manages, how much another tool such as CloudFormation manages, and how much neither does. It breaks this down by type, and by account and region when the import covers more than one (`scopes` in `coverage.json`). State can hold secrets: infraharvest reads it only when asked, keeps only resource types and IDs, and needs read access to the state for it. A source that names no bucket, container, organization or workspace, or a local path that isn't there, fails the command before anything is listed, as does a `--selection` file or `--catalogue` directory that doesn't load. So does a source that holds no state once read: a directory without `.tfstate` files, a prefix without `.tfstate` objects, or a workspace pattern that matches no workspace.

When part of an estate is already under Terraform, whether written by hand or imported earlier with another tool, point `--managed-state` at that state before importing the rest. What it manages is then left out, so nothing is imported twice, and the new roots hold only what no state has yet:

```sh
infraharvest import aws --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml \
  --managed-state=./existing-roots
```

A run that fails part way, such as on one region, can be run again with `--resume`: roots generated from the same resources and options since then are kept as they are, with their results. The others are generated again from scratch. Checkpoints go into `<out>/.infraharvest`, which `.gitignore` excludes. A root directory that already holds `.tf` files no checkpoint says an import wrote, such as an unrelated root, is refused rather than overwritten, and `--resume` only removes files from roots a checkpoint says an import wrote.

Once the roots are in use, `--incremental` adds what is new to them instead, without changing what they have. Each new resource goes after the blocks of the file of its type, such as `network.tf` for a subnet, which keeps the blocks it has as they are, or into a new file of that name; one that has no such file goes into a file of its own, `generated_2.tf`, then `generated_3.tf` and so on. Its import blocks, variables, locals and data sources are added after the root's own, and nothing new takes a name the root uses. New resources refer to the resources the root has, such as `vpc_id = aws_vpc.main.id`. They don't repeat the tags the root's provider already applies, and those without one of them use the provider's second configuration, without `default_tags`, which the root gets if it has none. A root's resources are found from its import blocks and from the checkpoint of the import that generated it; for roots applied since, with their import blocks deleted, add `--managed-state` too. A root whose import didn't finish, with no checkpoint of a result or, without a checkpoint, a `generated.tf` but no README, isn't added to: it is reported as failed, and `--resume` generates it again first. The new resources are generated and planned in a directory of their own first, because planning the root itself would need its state. In the root, the other checks run on the files as merged. Clusters of new resources can become curated module calls, but they aren't moved into generated local modules.

```sh
infraharvest discover aws --regions=eu-west-2 --selection=selection.yaml   # marks what is new
infraharvest import aws --regions=eu-west-2 --selection=selection.yaml --incremental --managed-state=backend
```

## Services

A provider lists its resources service by service, and `--services` (`-s`) says which services: `--services=vpc,subnet,sg,s3`. Every `import` and `discover` needs it, on the command line or in the [configuration file](configuration.md). `--services='*'` stands for all of the provider's services except the opt-in ones (quote the `*` so the shell leaves it alone). `--skip` then leaves services out, whether they were named one by one or through `*`:

```sh
infraharvest discover aws --services='*' --skip=iam,organization --regions=eu-west-2
```

`infraharvest services <provider>` prints a provider's services, and the [services reference](reference/services.md) gives the Terraform types each one lists. A service name the provider doesn't have fails like a service that can't be listed. Azure's opt-in services list what `resource_graph` lists already, so they are listed only when `--services` names them: each lists one kind of resource through Resource Manager, such as `virtual_network` (see [Azure](azure.md)).

Services are listed several at a time (`--list-parallel`, 4 by default). One that fails, or takes longer than `--list-timeout` (30 minutes by default), is left out of the output and reported, and the run exits with 1, or with 3 under `--allow-partial` (see [Exit codes](output.md#exit-codes)). One that listed but failed on a part, such as `route53` when the records of a hosted zone fail to list, keeps what it listed, and is reported and sets the exit code the same way. So do tags that fail to read, as when `tag:GetResources` is denied: selection rules by tag, owner detection, `{env}` layouts and the report's counts by tag would miss them, so the run goes on with what it has but doesn't pass as complete.

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

**Tags and other attributes.** `--tag <key>=<value>[,<value>...]` keeps the resources whose tag has one of the values, and `--tag <key>` those that have the tag at all. `--where <path>=<value>[,<value>...]` does the same for the attribute at a dotted path, as the lister records it, such as `tags.Environment` (what `--tag Environment` filters on). They apply to every type. The listers that support them apply them as they list, and the others ignore them, so the import logs a warning for each. Only these AWS listers support them, on tags:

| Service | Example |
|---|---|
| `ec2_instance` | `--tag Environment=prod` |
| `ebs` | `--tag team=web,data` |
| `api_gateway` | `--tag owner=payments` |

A REST API left out by a filter takes its stages, resources, methods and the rest with it. To choose resources by tag on any service, use the selection file's rules instead (see [Choosing what to import](#choosing-what-to-import)), which match the tags discover reads.

# Command-line reference

<!-- Generated from the commands by TestGoldenCLIReference in internal/cli/clireference_test.go: don't edit. -->

Every provider has the same `import` and `discover` flags, and flags of its own. This page lists AWS's, Azure's and Google Cloud's; `infraharvest import <provider> --help` lists any provider's.

## infraharvest bootstrap

Write a root that creates the state backend named in the configuration file

```
infraharvest bootstrap [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--config` |  | configuration file with the backend (required) |
| `-o`, `--out` | `infraharvest-out` | directory to write the bootstrap root under |

## infraharvest cfn

Translate CloudFormation templates into Terraform

```
infraharvest cfn
```

## infraharvest cfn convert

Translate a CloudFormation template into Terraform files

```
infraharvest cfn convert <template> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--allow-partial` |  | exit 3 rather than 1 when something couldn't be converted |
| `-o`, `--out` | `cfn-terraform` | directory to write the Terraform files into; files of the same names must not exist |
| `--prevent-destroy` |  | add lifecycle prevent_destroy to resources whose DeletionPolicy is Retain |
| `--stack-name` |  | default of the stack_name variable that stands in for AWS::StackName |

## infraharvest discover

List resources into a selection file to review before importing

```
infraharvest discover
```

| Flag | Default | Description |
|---|---|---|
| `--config` |  | configuration file, which sets flags not given on the command line |
| `--pick` | `true` | open the picker on the selection file once it is written, when run in a terminal (see infraharvest pick) |

## infraharvest discover aws

List aws resources into a selection file

```
infraharvest discover aws
```

| Flag | Default | Description |
|---|---|---|
| `--accounts` |  | accounts to import, each through the role --assume-role names |
| `--all` |  | import everything the default selection includes, without a selection file |
| `--allow-partial` |  | keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1 |
| `--assume-role` | `arn:aws:iam::{account}:role/infraharvest-readonly` | role to assume in each account of --accounts or --organization; {account} stands for the account ID. Without them, a role ARN assumes that role |
| `--catalogue` |  | directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--cfn-ignore` |  | with --cfn-stack: acknowledge that the stacks may be deleted without this resource, which Terraform can't import, such as a custom resource, by its logical ID, or as <stack>/<logical ID> where several stacks have it; repeat for others. It still needs Retain, as the copy of the template with Retain added gives it, so that deleting the stack never calls its handler. The CDK's Custom::S3AutoDeleteObjects and the like, which destroy what the stack's resources hold, can't be acknowledged (see https://ignatg.github.io/infraharvest/aws/#custom-resources) |
| `--cfn-stack` |  | import the resources of this CloudFormation stack, and of the stacks nested in it, and nothing else, by name or stack ID (ARN; a deleted stack's for 90 days); repeat for other stacks. The report says whether each stack can be deleted once its resources are in Terraform, and writes a copy of its template with Retain added (see https://ignatg.github.io/infraharvest/aws/#migrating-a-cloudformation-stack) |
| `--check` |  | check the flags and exit without listing anything: what the configuration file sets too, and the selection file, managed state sources and catalogue they name |
| `--config` |  | configuration file, which sets flags not given on the command line |
| `--default-env` |  | environment, such as dev, of the resources no --env-tags tag places, which otherwise go to {out}/envs/shared/ |
| `--engine` | `terraform` | terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit) |
| `--env-tags` |  | tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env) |
| `--force-unlock` |  | write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself |
| `--format` | `text` | text, or json to also print the import report as JSON on stdout (discover: a summary of what it listed, by what manages it) |
| `--id` |  | keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as vpc=vpc-0a1b2c3d; repeat for other types (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--include-owned` |  | import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default |
| `--incremental` |  | add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have |
| `--layout` | `{out}/{provider}/{account}/{region}/` | directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags) |
| `--list-parallel` | `4` | how many services of an account and region, a subscription or a project to list at once |
| `--list-timeout` | `30m0s` | longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit) |
| `--managed-state` |  | leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state |
| `--merge-environments` | `true` | with environments: in the configuration file, merge the roots each stack (provider, region and {service}) has in its environments into one root they share, with what differs in vars/<env>/, where every environment's plan of it stays the same; false keeps a root per environment (see https://ignatg.github.io/infraharvest/configuration/#environments) |
| `--modules` | `registry` | registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root |
| `--organization` |  | import every active account of the organization (needs organizations:ListAccounts), each through the role --assume-role names |
| `-o`, `--out` | `infraharvest-out` | directory to write the generated roots and the report to |
| `--parallel` | `1` | how many accounts of --accounts or --organization and regions of --regions to import at once, in all; each runs its own Terraform, so memory use grows with it |
| `--pick` | `true` | open the picker on the selection file once it is written, when run in a terminal (see infraharvest pick) |
| `--profile` | `default` | AWS profile, from the shared configuration and credentials files, whose credentials the import uses |
| `--rate-limit` | `20` | most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit) |
| `--regenerate` |  | generate the roots earlier imports generated in --out again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed |
| `--regions` |  | regions to import, each into roots of its own, such as eu-west-1,us-east-1 (default: the region of the profile or the environment) |
| `--report-tags` |  | tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key (discover: in its log and JSON summary) |
| `--resource-explorer` |  | don't list the services that AWS Resource Explorer's index shows have nothing in a region (needs an index and a default view; the index can miss resources created in the last hours, see https://ignatg.github.io/infraharvest/aws/#resource-explorer) |
| `--resume` |  | skip the roots a previous run generated from the same resources and options, and generate again those it didn't finish, such as after a run that failed part way |
| `--reuse-inventory` |  | import from the resources infraharvest discover listed into the same --out, instead of listing them again |
| `--selection` |  | selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml) |
| `-s`, `--services` |  | services to import, such as vpc,s3, or "*" for all (infraharvest services <provider> lists them) |
| `--skip` |  | services to leave out of --services, such as vpc,s3 |
| `--tag` |  | keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6) |
| `-v`, `--verbose` |  | log more, such as the requests to the cloud's APIs |
| `--where` |  | keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |

## infraharvest discover azure

List azure resources into a selection file

```
infraharvest discover azure
```

| Flag | Default | Description |
|---|---|---|
| `--all` |  | import everything the default selection includes, without a selection file |
| `--allow-partial` |  | keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1 |
| `--azapi` |  | import what Resource Graph lists that azurerm has no resource type for as azapi_resource, through the Azure/azapi provider, instead of leaving it out |
| `--catalogue` |  | directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--check` |  | check the flags and exit without listing anything: what the configuration file sets too, and the selection file, managed state sources and catalogue they name |
| `--config` |  | configuration file, which sets flags not given on the command line |
| `--default-env` |  | environment, such as dev, of the resources no --env-tags tag places, which otherwise go to {out}/envs/shared/ |
| `--engine` | `terraform` | terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit) |
| `--env-tags` |  | tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env) |
| `--force-unlock` |  | write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself |
| `--format` | `text` | text, or json to also print the import report as JSON on stdout (discover: a summary of what it listed, by what manages it) |
| `--id` |  | keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as management_lock=LOCK_ID; repeat for other types (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--include-owned` |  | import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default |
| `--incremental` |  | add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have |
| `--layout` | `{out}/{provider}/{account}/{region}/` | directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags) |
| `--list-parallel` | `4` | how many services of an account and region, a subscription or a project to list at once |
| `--list-timeout` | `30m0s` | longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit) |
| `--managed-state` |  | leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state |
| `--management-group` |  | import every enabled subscription under this management group ID, in its child groups too (needs Reader on the group) |
| `--merge-environments` | `true` | with environments: in the configuration file, merge the roots each stack (provider, region and {service}) has in its environments into one root they share, with what differs in vars/<env>/, where every environment's plan of it stays the same; false keeps a root per environment (see https://ignatg.github.io/infraharvest/configuration/#environments) |
| `--modules` | `registry` | registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root |
| `-o`, `--out` | `infraharvest-out` | directory to write the generated roots and the report to |
| `--parallel` | `1` | how many subscriptions of --subscriptions or --management-group to import at once, in all; each runs its own Terraform, so memory use grows with it |
| `--pick` | `true` | open the picker on the selection file once it is written, when run in a terminal (see infraharvest pick) |
| `--rate-limit` | `20` | most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit) |
| `--regenerate` |  | generate the roots earlier imports generated in --out again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed |
| `--report-tags` |  | tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key (discover: in its log and JSON summary) |
| `-g`, `--resource-group` |  | resource group to import (default: the whole subscription) |
| `--resume` |  | skip the roots a previous run generated from the same resources and options, and generate again those it didn't finish, such as after a run that failed part way |
| `--reuse-inventory` |  | import from the resources infraharvest discover listed into the same --out, instead of listing them again |
| `--selection` |  | selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml) |
| `-s`, `--services` |  | services to import, such as resource_graph,management_lock, or "*" for all (infraharvest services <provider> lists them) |
| `--skip` |  | services to leave out of --services, such as resource_graph,management_lock |
| `--subscriptions` |  | subscription IDs to import, each into roots of its own (default ARM_SUBSCRIPTION_ID) |
| `--tag` |  | keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6) |
| `-v`, `--verbose` |  | log more, such as the requests to the cloud's APIs |
| `--where` |  | keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |

## infraharvest discover google

List google resources into a selection file

```
infraharvest discover google
```

| Flag | Default | Description |
|---|---|---|
| `--all` |  | import everything the default selection includes, without a selection file |
| `--allow-partial` |  | keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1 |
| `--beta` |  | generate configuration for the google-beta provider instead of google, for resources and arguments only it has |
| `--catalogue` |  | directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--check` |  | check the flags and exit without listing anything: what the configuration file sets too, and the selection file, managed state sources and catalogue they name |
| `--config` |  | configuration file, which sets flags not given on the command line |
| `--default-env` |  | environment, such as dev, of the resources no --env-tags tag places, which otherwise go to {out}/envs/shared/ |
| `--engine` | `terraform` | terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit) |
| `--env-tags` |  | tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env) |
| `--folders` |  | import every active project of these folder IDs, in their subfolders too |
| `--force-unlock` |  | write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself |
| `--format` | `text` | text, or json to also print the import report as JSON on stdout (discover: a summary of what it listed, by what manages it) |
| `--id` |  | keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as storage_bucket=my-logs; repeat for other types (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--include-owned` |  | import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default |
| `--incremental` |  | add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have |
| `--layout` | `{out}/{provider}/{account}/{region}/` | directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags) |
| `--list-parallel` | `4` | how many services of an account and region, a subscription or a project to list at once |
| `--list-timeout` | `30m0s` | longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit) |
| `--managed-state` |  | leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state |
| `--merge-environments` | `true` | with environments: in the configuration file, merge the roots each stack (provider, region and {service}) has in its environments into one root they share, with what differs in vars/<env>/, where every environment's plan of it stays the same; false keeps a root per environment (see https://ignatg.github.io/infraharvest/configuration/#environments) |
| `--modules` | `registry` | registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root |
| `--organization` |  | import every active project of this organization ID, in its folders too (needs resourcemanager.projects.list and folders.list, as roles/browser grants) |
| `-o`, `--out` | `infraharvest-out` | directory to write the generated roots and the report to |
| `--parallel` | `1` | how many projects of --projects, --organization or --folders and regions of --regions to import at once, in all; each runs its own Terraform, so memory use grows with it |
| `--pick` | `true` | open the picker on the selection file once it is written, when run in a terminal (see infraharvest pick) |
| `--projects` |  | projects to import |
| `--rate-limit` | `20` | most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit) |
| `--regenerate` |  | generate the roots earlier imports generated in --out again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed |
| `--regions` | `[global]` | regions to import, each into roots of its own, such as global,europe-west1; global holds the resources that belong to no region |
| `--report-tags` |  | tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key (discover: in its log and JSON summary) |
| `--resume` |  | skip the roots a previous run generated from the same resources and options, and generate again those it didn't finish, such as after a run that failed part way |
| `--reuse-inventory` |  | import from the resources infraharvest discover listed into the same --out, instead of listing them again |
| `--selection` |  | selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml) |
| `-s`, `--services` |  | services to import, such as cloud_asset,iam, or "*" for all (infraharvest services <provider> lists them) |
| `--skip` |  | services to leave out of --services, such as cloud_asset,iam |
| `--tag` |  | keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6) |
| `-v`, `--verbose` |  | log more, such as the requests to the cloud's APIs |
| `--where` |  | keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |

## infraharvest drift

Find what changed in the cloud since the generated roots were applied

```
infraharvest drift [output-directory] [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--accept` |  | write the cloud's values of the changes this rule matches into the code: all, or attribute=, type=, address= or root= with a pattern, such as attribute=tags.* (repeatable; with --keep, the last rule that matches a change decides) |
| `--catalogue` |  | directory of catalogue files the import used to add to or override the built-in catalogue, whose arguments kept only in state aren't drift (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--decisions` |  | decide the changes by this decisions file, which --write-decisions writes: each change's entry, else its rules and defaults; --accept and --keep decide after it |
| `--detailed-exitcode` |  | exit with 4 when a change is left that the next apply reverts or that can't be assessed, 1 when an accepted change failed verification, 0 when none |
| `--engine` | `terraform` | terraform or tofu |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the refresh of one root, may run (0: no limit) |
| `--environment` |  | for a root environments share, the environments to check it as, each with its backend settings and var files, with the credentials drift runs with, or all for every one under its vars/, if they reach every one; roots of more than one environment need it |
| `--force-unlock` |  | check even when another run holds the output directory's lock, as when that run is stuck, or when the file system can't lock |
| `--format` | `text` | text prints a line per root; json prints drift.json |
| `--keep` |  | keep the code's values of the changes this rule matches, which the next apply puts back, as it does for changes no rule matches: all, or a rule as --accept takes it (repeatable) |
| `--parallel` | `1` | how many roots to check at once; each runs its own Terraform, so memory use grows with it |
| `--root` |  | check only the roots whose path in the output directory, or a directory it is in, matches this pattern, such as aws/*/eu-west-2 (repeatable) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH, else installed as for import) |
| `--write-decisions` |  | write this run's changes into the decisions file, --write-decisions=<file>, else --decisions, else drift.yaml in the output directory, which isn't applied unless --decisions names it: entries whose change is unchanged keep their decisions, new changes and those that changed again are undecided, and changes no longer found are dropped |

## infraharvest import

Generate Terraform configuration for existing cloud resources

```
infraharvest import
```

| Flag | Default | Description |
|---|---|---|
| `--config` |  | configuration file, which sets flags not given on the command line and the state backend of the generated roots |

## infraharvest import aws

Generate Terraform configuration for the resources of AWS accounts

```
infraharvest import aws
```

| Flag | Default | Description |
|---|---|---|
| `--accounts` |  | accounts to import, each through the role --assume-role names |
| `--all` |  | import everything the default selection includes, without a selection file |
| `--allow-partial` |  | keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1 |
| `--assume-role` | `arn:aws:iam::{account}:role/infraharvest-readonly` | role to assume in each account of --accounts or --organization; {account} stands for the account ID. Without them, a role ARN assumes that role |
| `--catalogue` |  | directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--cfn-ignore` |  | with --cfn-stack: acknowledge that the stacks may be deleted without this resource, which Terraform can't import, such as a custom resource, by its logical ID, or as <stack>/<logical ID> where several stacks have it; repeat for others. It still needs Retain, as the copy of the template with Retain added gives it, so that deleting the stack never calls its handler. The CDK's Custom::S3AutoDeleteObjects and the like, which destroy what the stack's resources hold, can't be acknowledged (see https://ignatg.github.io/infraharvest/aws/#custom-resources) |
| `--cfn-stack` |  | import the resources of this CloudFormation stack, and of the stacks nested in it, and nothing else, by name or stack ID (ARN; a deleted stack's for 90 days); repeat for other stacks. The report says whether each stack can be deleted once its resources are in Terraform, and writes a copy of its template with Retain added (see https://ignatg.github.io/infraharvest/aws/#migrating-a-cloudformation-stack) |
| `--check` |  | check the flags and exit without listing anything: what the configuration file sets too, and the selection file, managed state sources and catalogue they name |
| `--config` |  | configuration file, which sets flags not given on the command line and the state backend of the generated roots |
| `--default-env` |  | environment, such as dev, of the resources no --env-tags tag places, which otherwise go to {out}/envs/shared/ |
| `--engine` | `terraform` | terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit) |
| `--env-tags` |  | tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env) |
| `--force-unlock` |  | write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself |
| `--format` | `text` | text, or json to also print the import report as JSON on stdout (discover: a summary of what it listed, by what manages it) |
| `--id` |  | keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as vpc=vpc-0a1b2c3d; repeat for other types (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--include-owned` |  | import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default |
| `--incremental` |  | add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have |
| `--layout` | `{out}/{provider}/{account}/{region}/` | directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags) |
| `--list-parallel` | `4` | how many services of an account and region, a subscription or a project to list at once |
| `--list-timeout` | `30m0s` | longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit) |
| `--managed-state` |  | leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state |
| `--merge-environments` | `true` | with environments: in the configuration file, merge the roots each stack (provider, region and {service}) has in its environments into one root they share, with what differs in vars/<env>/, where every environment's plan of it stays the same; false keeps a root per environment (see https://ignatg.github.io/infraharvest/configuration/#environments) |
| `--modules` | `registry` | registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root |
| `--organization` |  | import every active account of the organization (needs organizations:ListAccounts), each through the role --assume-role names |
| `-o`, `--out` | `infraharvest-out` | directory to write the generated roots and the report to |
| `--parallel` | `1` | how many accounts of --accounts or --organization and regions of --regions to import at once, in all; each runs its own Terraform, so memory use grows with it |
| `--profile` | `default` | AWS profile, from the shared configuration and credentials files, whose credentials the import uses |
| `--rate-limit` | `20` | most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit) |
| `--regenerate` |  | generate the roots earlier imports generated in --out again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed |
| `--regions` |  | regions to import, each into roots of its own, such as eu-west-1,us-east-1 (default: the region of the profile or the environment) |
| `--report-tags` |  | tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key (discover: in its log and JSON summary) |
| `--resource-explorer` |  | don't list the services that AWS Resource Explorer's index shows have nothing in a region (needs an index and a default view; the index can miss resources created in the last hours, see https://ignatg.github.io/infraharvest/aws/#resource-explorer) |
| `--resume` |  | skip the roots a previous run generated from the same resources and options, and generate again those it didn't finish, such as after a run that failed part way |
| `--reuse-inventory` |  | import from the resources infraharvest discover listed into the same --out, instead of listing them again |
| `--selection` |  | selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml) |
| `-s`, `--services` |  | services to import, such as vpc,s3, or "*" for all (infraharvest services <provider> lists them) |
| `--skip` |  | services to leave out of --services, such as vpc,s3 |
| `--tag` |  | keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6) |
| `-v`, `--verbose` |  | log more, such as the requests to the cloud's APIs |
| `--where` |  | keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |

## infraharvest import azure

Generate Terraform configuration for the resources of Azure subscriptions

```
infraharvest import azure
```

| Flag | Default | Description |
|---|---|---|
| `--all` |  | import everything the default selection includes, without a selection file |
| `--allow-partial` |  | keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1 |
| `--azapi` |  | import what Resource Graph lists that azurerm has no resource type for as azapi_resource, through the Azure/azapi provider, instead of leaving it out |
| `--catalogue` |  | directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--check` |  | check the flags and exit without listing anything: what the configuration file sets too, and the selection file, managed state sources and catalogue they name |
| `--config` |  | configuration file, which sets flags not given on the command line and the state backend of the generated roots |
| `--default-env` |  | environment, such as dev, of the resources no --env-tags tag places, which otherwise go to {out}/envs/shared/ |
| `--engine` | `terraform` | terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit) |
| `--env-tags` |  | tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env) |
| `--force-unlock` |  | write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself |
| `--format` | `text` | text, or json to also print the import report as JSON on stdout (discover: a summary of what it listed, by what manages it) |
| `--id` |  | keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as management_lock=LOCK_ID; repeat for other types (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--include-owned` |  | import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default |
| `--incremental` |  | add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have |
| `--layout` | `{out}/{provider}/{account}/{region}/` | directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags) |
| `--list-parallel` | `4` | how many services of an account and region, a subscription or a project to list at once |
| `--list-timeout` | `30m0s` | longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit) |
| `--managed-state` |  | leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state |
| `--management-group` |  | import every enabled subscription under this management group ID, in its child groups too (needs Reader on the group) |
| `--merge-environments` | `true` | with environments: in the configuration file, merge the roots each stack (provider, region and {service}) has in its environments into one root they share, with what differs in vars/<env>/, where every environment's plan of it stays the same; false keeps a root per environment (see https://ignatg.github.io/infraharvest/configuration/#environments) |
| `--modules` | `registry` | registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root |
| `-o`, `--out` | `infraharvest-out` | directory to write the generated roots and the report to |
| `--parallel` | `1` | how many subscriptions of --subscriptions or --management-group to import at once, in all; each runs its own Terraform, so memory use grows with it |
| `--rate-limit` | `20` | most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit) |
| `--regenerate` |  | generate the roots earlier imports generated in --out again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed |
| `--report-tags` |  | tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key (discover: in its log and JSON summary) |
| `-g`, `--resource-group` |  | resource group to import (default: the whole subscription) |
| `--resume` |  | skip the roots a previous run generated from the same resources and options, and generate again those it didn't finish, such as after a run that failed part way |
| `--reuse-inventory` |  | import from the resources infraharvest discover listed into the same --out, instead of listing them again |
| `--selection` |  | selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml) |
| `-s`, `--services` |  | services to import, such as resource_graph,management_lock, or "*" for all (infraharvest services <provider> lists them) |
| `--skip` |  | services to leave out of --services, such as resource_graph,management_lock |
| `--subscriptions` |  | subscription IDs to import, each into roots of its own (default ARM_SUBSCRIPTION_ID) |
| `--tag` |  | keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6) |
| `-v`, `--verbose` |  | log more, such as the requests to the cloud's APIs |
| `--where` |  | keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |

## infraharvest import google

Generate Terraform configuration for the resources of Google Cloud projects

```
infraharvest import google
```

| Flag | Default | Description |
|---|---|---|
| `--all` |  | import everything the default selection includes, without a selection file |
| `--allow-partial` |  | keep going when some services or resources fail to import, leaving them out of the output, and exit 3 instead of 1 |
| `--beta` |  | generate configuration for the google-beta provider instead of google, for resources and arguments only it has |
| `--catalogue` |  | directory of catalogue files (YAML) that add to or override what the built-in catalogue says about resource types: import IDs, arguments to leave out, default skips, data sources (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--check` |  | check the flags and exit without listing anything: what the configuration file sets too, and the selection file, managed state sources and catalogue they name |
| `--config` |  | configuration file, which sets flags not given on the command line and the state backend of the generated roots |
| `--default-env` |  | environment, such as dev, of the resources no --env-tags tag places, which otherwise go to {out}/envs/shared/ |
| `--engine` | `terraform` | terraform or tofu: generate configuration with Terraform or OpenTofu from import blocks |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run; a root whose command takes longer fails (0: no limit) |
| `--env-tags` |  | tag or label keys, such as Environment, whose value names a resource's environment: roots per environment, under {out}/envs/{env}/, for {env} in --layout (default for {env}: Environment, environment, Env, env) |
| `--folders` |  | import every active project of these folder IDs, in their subfolders too |
| `--force-unlock` |  | write to --out even when another run holds its lock, as when that run is stuck, or when the file system can't lock; a run that is killed releases the lock by itself |
| `--format` | `text` | text, or json to also print the import report as JSON on stdout (discover: a summary of what it listed, by what manages it) |
| `--id` |  | keep only the resources of a type with these IDs, as <type>=<id>[,<id>...] such as storage_bucket=my-logs; repeat for other types (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--include-owned` |  | import resources whose tags or labels say another Terraform state or tool manages them (ManagedBy=Terraform, Pulumi, CloudFormation ..., aws:cloudformation:stack-name, goog-terraform-provisioned), which are left out by default |
| `--incremental` |  | add what is new to the roots earlier imports generated in --out, after the blocks of the file of its type, without changing what they have |
| `--layout` | `{out}/{provider}/{account}/{region}/` | directories of the generated roots, one root each, from {out}, {provider} (aws, azure or google), {account}, {region}, {service} and {env} (see --env-tags) |
| `--list-parallel` | `4` | how many services of an account and region, a subscription or a project to list at once |
| `--list-timeout` | `30m0s` | longest time to list one service in one account and region, subscription or project; a service that takes longer is reported as failed (0: no limit) |
| `--managed-state` |  | leave out what Terraform already manages, according to this state: state files, directories of them, or s3://bucket/prefix[?region=...&profile=...] (read with --profile unless it names one) gs://bucket/prefix or https://<account>.blob.core.windows.net/<container>/prefix (all their .tfstate objects), or tfc://<organization>/<workspace> (<prefix>* for several, ?host= for Terraform Enterprise); backend reads the configured S3, GCS or azurerm backend's state |
| `--merge-environments` | `true` | with environments: in the configuration file, merge the roots each stack (provider, region and {service}) has in its environments into one root they share, with what differs in vars/<env>/, where every environment's plan of it stays the same; false keeps a root per environment (see https://ignatg.github.io/infraharvest/configuration/#environments) |
| `--modules` | `registry` | registry moves clusters of resources into curated public modules (terraform-aws-modules, terraform-google-modules and Azure Verified Modules), at the release each adapter is tested with, where the plan stays the same, else into generated local modules; latest-untested calls each module's newest release instead; local uses generated local modules only; none keeps every resource in the root |
| `--organization` |  | import every active project of this organization ID, in its folders too (needs resourcemanager.projects.list and folders.list, as roles/browser grants) |
| `-o`, `--out` | `infraharvest-out` | directory to write the generated roots and the report to |
| `--parallel` | `1` | how many projects of --projects, --organization or --folders and regions of --regions to import at once, in all; each runs its own Terraform, so memory use grows with it |
| `--projects` |  | projects to import |
| `--rate-limit` | `20` | most requests a second to each cloud API: per API host, and for AWS and Azure per account or subscription too (0: no limit) |
| `--regenerate` |  | generate the roots earlier imports generated in --out again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed |
| `--regions` | `[global]` | regions to import, each into roots of its own, such as global,europe-west1; global holds the resources that belong to no region |
| `--report-tags` |  | tag keys, such as owner,team, to count what was discovered, imported and managed by in the report, one table per key (discover: in its log and JSON summary) |
| `--resume` |  | skip the roots a previous run generated from the same resources and options, and generate again those it didn't finish, such as after a run that failed part way |
| `--reuse-inventory` |  | import from the resources infraharvest discover listed into the same --out, instead of listing them again |
| `--selection` |  | selection file from infraharvest discover, saying which resources to import (discover: the file to write, default selection.yaml) |
| `-s`, `--services` |  | services to import, such as cloud_asset,iam, or "*" for all (infraharvest services <provider> lists them) |
| `--skip` |  | services to leave out of --services, such as cloud_asset,iam |
| `--tag` |  | keep only the resources with this tag, as <key>[=<value>[,<value>...]] such as env=prod, where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH; Terraform >= 1.5 or else the latest release, downloaded and verified; OpenTofu >= 1.6) |
| `-v`, `--verbose` |  | log more, such as the requests to the cloud's APIs |
| `--where` |  | keep only the resources whose attribute at a dotted path has one of these values, as <path>[=<value>[,<value>...]], where the service's lister supports it (see https://ignatg.github.io/infraharvest/usage/#filtering) |

## infraharvest mcp

Serve infraharvest to AI agents over the Model Context Protocol (stdio)

```
infraharvest mcp [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--root` |  | directory the tools may read and write files in: selection files, output directories, reports and drift decisions files (default: the current directory, unless that is the root of a filesystem or the home directory) |

## infraharvest pick

Choose in the terminal what a selection file imports

```
infraharvest pick [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--selection` | `selection.yaml` | selection file to edit, from infraharvest discover |

## infraharvest report

Print the report of an import (default directory: infraharvest-out)

```
infraharvest report [output-directory] [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--format` | `text` | text prints the report as Markdown; json as JSON |

## infraharvest services

List the services of a provider that --services takes

```
infraharvest services <provider>
```

## infraharvest verify

Check generated roots again: format, validate, plan, standards, scanners, secrets, determinism

```
infraharvest verify [output-directory] [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--catalogue` |  | directory of catalogue files the import used to add to or override the built-in catalogue (see https://ignatg.github.io/infraharvest/catalogue/) |
| `--engine` | `terraform` | terraform or tofu |
| `--engine-timeout` | `1h0m0s` | longest time one Terraform or OpenTofu command, such as the plan of one root, may run (0: no limit) |
| `--environment` |  | for a root environments share, the environments to plan it as, each with its var files, with the credentials verify runs with (default: every environment under its vars/) |
| `--force-unlock` |  | verify even when another run holds the output directory's lock, as when that run is stuck, or when the file system can't lock |
| `--format` | `text` | text prints a summary; json prints each root's checks as JSON |
| `--terraform-path` |  | Terraform or OpenTofu binary (default: on PATH, else installed as for import) |

## infraharvest version

Print which version of infraharvest this is

```
infraharvest version
```

# End-to-end tests

These tests run infraharvest against local cloud emulators, so they need no cloud account and cost nothing. CI runs them on every pull request, except the sandbox tests, which run against a real AWS account, Azure subscription or Google Cloud project, and only by hand (see [AWS sandbox](#aws-sandbox-a-real-account), [Azure sandbox](#azure-sandbox-a-real-subscription) and [Google Cloud sandbox](#google-cloud-sandbox-a-real-project)).

A test whose emulator or sandbox isn't set up skips. CI sets `INFRAHARVEST_E2E_REQUIRED=1`, which makes it fail instead, so a broken setup doesn't pass as a run that tested nothing.

## AWS (Floci)

[`TestAWSRoundTrip`](aws_test.go) uses [Floci](https://github.com/floci-io/floci), an open-source AWS emulator:

1. Terraform creates the resources in [`testdata/aws`](testdata/aws/main.tf) in Floci.
2. `infraharvest import aws --all` imports them.
3. Terraform plans the generated configuration, with the secret variables set to the values from step 1. Every resource must be an import with no changes, no resource may be left out (`rejected.hcl`), and every resource type created in step 1 must be imported.

Two more tests import the same resources: `TestAWSSelection` imports again from the selection file `infraharvest discover aws` writes, which must give the same files as `--all` byte for byte, and `TestAWSIncremental` adds what a selection left out with `--incremental`. CI runs each on a runner of its own; run in one process, they share the resources the first one creates.

[`TestAWSDrift`](drift_aws_test.go) checks `infraharvest drift`, on a runner of its own, with an account of its own. It imports two queues and a bucket ([`testdata/aws-drift`](testdata/aws-drift/main.tf)), which share a `Project` tag, applies the roots into local states, and changes the resources out of band by applying the fixture again with other variables. A queue's tag and retention changed, drift must report both as `kept`, exit with 4 with `--detailed-exitcode`, write `drift.json` (`schema_version` 1) and `drift.md`, and leave the code as it was. With `--accept` and `--keep`, the last rule that matches a change decides it. `--write-decisions` must write the retention's entry as `undecided`; set to `accept`, it must be kept and marked `decision_stale` once the retention changes again, and, set to `accept` again, be written. The `Project` tag changed on every resource must be one change of `local.tags`, covering both queues, and the bucket's own tag, in a call of the s3-bucket module, a change traced to the call's `tags` (`source.module_path`); accepted, both are written there. The code must then plan no change, with a refresh and, once the state is refreshed, without one, and drift must find nothing.


Two tests create resources of their own, in accounts no other test uses, and share a runner in CI: [`TestAWSParallelAccounts`](accounts_test.go) imports two accounts at once (`--accounts`, `--parallel=2`), and [`TestAWSParallelRegions`](regions_test.go) imports the global services and two regions of an account ([`testdata/aws-regions`](testdata/aws-regions/main.tf): a role and a log group in each region) twice, with `--parallel=1` and `--parallel=2`. The log must show the two regions listing or generating at the same time only with `--parallel=2` (from `aws: importing resources in region …` to `aws: listed resources in region …`, and from `aws: generating the roots in region …` to `aws: generated the roots in region …`), both imports must write the same files, and every root must plan as imports with no changes.

[`TestAWSRegenerateMoves`](moves_test.go) shares that runner too, with an account of its own. It imports three queues ([`testdata/aws-moves`](testdata/aws-moves/main.tf)) into one root per environment (`--env-tags=Env`) and applies the `dev` root into a local state. It then gives one queue another `Name` tag, so another address, and another the `prod` environment, so another root, and imports again with `--regenerate`. The third queue must keep its address. `moves.tf` of the `dev` root must have a `moved` block for the first queue and a `removed` block, with `destroy = false`, for the second; the gate, G8 included, must pass; the `prod` root must import the second queue; both READMEs must name the other root; and `coverage.json` must list them under `moved` and `departed`. Planned over the state the first apply left, the `dev` root must show the first queue as moved and forget the second, creating and destroying nothing. Applied, then the `prod` root applied, neither root may plan any change.

The options tests ([`options_test.go`](options_test.go) and [`options_input_test.go`](options_input_test.go)) run the command-line options on [`testdata/aws-options`](testdata/aws-options/main.tf), created in an account of its own (`444444444444`), and check each option's effect on the output, with every root planning as imports with no changes:

- `TestAWSOptionsLayout`: `--layout={out}/{provider}/{account}/{region}/{service}/` puts each service's resources in a root of its own, the subnet and security group read the VPC of another root through a data source, and the default `--modules` (registry) makes the buckets calls of the curated s3-bucket module at its tested version, and the roles calls of one generated local module.
- `TestAWSOptionsModules`: `--modules=none` writes no module call or module, `--modules=local` only generated local modules, and `--modules=latest-untested` calls the s3-bucket module's newest release. `infraharvest verify` passes on the `none` output, then exits with 1 once a generated file says another queue retention than the queue has.
- `TestAWSOptionsReport`: `--format json` prints the report on stdout as one JSON document, which `infraharvest report --format json` prints again, as `infraharvest report` prints `report.md`; `--report-tags` counts by a tag; `--list-parallel=1` lists one service at a time; a `--catalogue` overlay ([`testdata/catalogue/skip`](testdata/catalogue/skip/aws.yaml)) leaves a topic out; and a queue tagged `ManagedBy=Terraform` is left out, unless `--include-owned`.
- `TestAWSOptionsFilters`: `--services`, `--skip`, `--id`, `--tag` and `--where` keep exactly what they should: the EBS lister filters volumes by tag, and the SQS lister, which doesn't support tag filters, lists every queue.
- `TestAWSOptionsExitCodes`: an overlay ([`testdata/catalogue/gone`](testdata/catalogue/gone/aws.yaml)) whose topic import ID names no topic makes the import partial, exiting with 1, or 3 with `--allow-partial`; `--engine-timeout=1ms` fails the root.

CI runs them on two runners, `TestAWSOptionsLayout` and `TestAWSOptionsModules` on one, the others on the other; run in one process, they share the resources the first one creates.

Run them locally (needs Docker and Terraform >= 1.5; compiling uses a few GB of RAM):

```sh
docker compose -f e2e/compose.yaml up -d --wait
AWS_ENDPOINT_URL=http://localhost:4566 go test -tags e2e,minimal,aws -timeout 40m -v ./e2e/
docker compose -f e2e/compose.yaml down
```

Set `E2E_ENGINE=tofu` to run the same test with OpenTofu >= 1.6 (`--engine=tofu`). CI runs it with both engines, because the output must work on both.

The tests don't delete what they create, so restart Floci (`down`, then `up`) before running them again.

The test uses test credentials and empty AWS config files, and refuses any `AWS_ENDPOINT_URL` other than localhost, so it never touches a real account.

To cover another service, add its resources to `testdata/aws/main.tf` and its infraharvest service name to `awsServices` in `aws_test.go`. Check Floci [supports the service](https://github.com/floci-io/floci/tree/main/docs/services) first.

Not covered yet:

- **Services Floci runs in Docker containers:** EC2 instances, Lambda, RDS and ElastiCache. The test would need the Docker socket mounted into Floci.
- **Lambda functions in general:** the generated configuration can't include the function code, so it doesn't validate until you add the package.

An emulator doesn't enforce IAM permissions or reproduce every AWS API quirk, so this doesn't replace a check against a real account.

## Google Cloud (floci-gcp)

[`TestGoogleRoundTrip`](google_test.go) uses [floci-gcp](https://github.com/floci-io/floci-gcp), the Google Cloud member of the same emulator family:

1. Terraform creates the resources in [`testdata/google`](testdata/google/main.tf) in floci-gcp: a VPC network with a subnetwork and a firewall rule, a Cloud Storage bucket, a Pub/Sub topic with a subscription, a service account, grants in the project's IAM policy, and a KMS key ring with keys.
2. `infraharvest import google --all` imports them: with `--services='*'`, and again with `--env-tags`.
3. Terraform plans the generated configuration. Every resource must be an import with no changes, and every resource type created in step 1 must be imported.

Five more tests import the same resources, each through a flow the round trip doesn't take:

- `TestGoogleDrift` applies the roots, changes the bucket's `team` label through the Cloud Storage API, and checks `infraharvest drift` reports it as `kept`, traced through the bucket's module call (`source.module_path`), exiting with 4 with `--detailed-exitcode`, and, with `--accept`, writes it into the call's `labels`; the code must then plan no change but to the labels the provider keeps only in state, and drift, once the state is refreshed, find nothing. Only the bucket has labels, so labels several resources share are left to the AWS test.
- `TestGoogleSelection` runs `infraharvest discover google`, checks the selection file and the inventories it saves list what step 1 created, and leaves out a firewall rule named like a default network's ([`testdata/google-selection`](testdata/google-selection/main.tf)). It then edits the file to exclude the Pub/Sub subscription and include that rule, and imports with `--selection`: the import must import what the file includes and nothing else, with no changes.
- `TestGoogleIncremental` imports, creates a Pub/Sub topic ([`testdata/google-incremental`](testdata/google-incremental/main.tf)) and imports again with `--incremental`: the topic must be added to global's root in a file of its own, with nothing else changed, and the roots must still plan with no changes.
- `TestGoogleResume` imports again with `--resume` into a finished import's output, which must generate no root again.
- `TestGoogleReuseInventory` imports with `--reuse-inventory` after discover: the import must take the resources from the inventories discover saved and make none of the listing requests (searches and reads of the IAM policy) discover made.

CI runs the round trip, `TestGoogleIncremental`, which adds to what the others import, and `TestGoogleDrift`, which changes it, on runners of their own, and the other three on a fourth; run in one process, they share the resources the first one creates.

```sh
docker compose -f e2e/compose.yaml --profile google up -d floci-gcp
INFRAHARVEST_GOOGLE_ENDPOINT=http://localhost:4588 go test -tags e2e,minimal,google -run TestGoogle -v ./e2e/
docker compose -f e2e/compose.yaml --profile google down
```

`INFRAHARVEST_GOOGLE_ENDPOINT` sends the listers' Google API calls to the emulator, without credentials. The test points the Terraform provider there through its `GOOGLE_*_CUSTOM_ENDPOINT` variables, with a placeholder access token. It refuses any endpoint other than localhost.

floci-gcp doesn't serve Cloud Asset Inventory or Cloud DNS. A stand-in in front of it answers `cloud_asset`'s searches from what step 1 created, as Cloud Asset Inventory names it, with the default network and one of its firewall rules, which the default selection must leave out, and finds no DNS zones; floci-gcp serves the rest, such as the IAM policy, the Cloud SQL and Cloud Scheduler lists, and what Terraform plans with. Not covered yet: DNS record sets, Cloud SQL databases and Cloud Scheduler jobs, which only unit tests cover, and the shapes of real Cloud Asset Inventory results, which the stand-in only imitates. The [Google Cloud sandbox](#google-cloud-sandbox-a-real-project) covers DNS record sets and real Cloud Asset Inventory results.

## Azure (floci-az)

[`TestAzureRoundTrip`](azure_test.go) uses [floci-az](https://github.com/floci-io/floci-az), the Azure member of the same emulator family:

1. Terraform creates the resources in [`testdata/azure`](testdata/azure/main.tf) in floci-az: a resource group with a virtual network and subnet, a network security group with a rule, a public IP, and a network interface.
2. `infraharvest import azure --all --resource-group=infraharvest-e2e` imports them. `--services` names the services that list through Resource Manager, since floci-az has no Resource Graph, which `--services=*` uses.
3. Terraform plans the generated configuration. Every resource must be an import with no changes, and every resource type created in step 1 must be imported.

Five more tests import the same resources, each through a flow the round trip doesn't take:

- `TestAzureDrift` applies the root, changes the network security group's `env` tag and the identity's `role` tag through a configuration of their own that imports them, and checks `infraharvest drift` reports both as `kept`, exiting with 4 with `--detailed-exitcode`, and, with `--accept`, writes the group's `env` over `local.tags`, which the other resources keep, and the identity's `role`, in its module call when the import made one. The code must then plan no change, and drift, once the state is refreshed, find nothing. floci-az doesn't keep the resource group's tags, so the tags all the resources share can't change together: the AWS test covers that.
- `TestAzureSelection` runs `infraharvest discover azure`, checks the selection file and the inventory it saves list what step 1 created, and leaves out a network security group whose tag says Pulumi manages it ([`testdata/azure-selection`](testdata/azure-selection/main.tf)). It then edits the file to exclude the public IP and include that group, and imports with `--selection`: the import must import what the file includes and nothing else, with no changes.
- `TestAzureIncremental` imports, creates a network security group ([`testdata/azure-incremental`](testdata/azure-incremental/main.tf)) and imports again with `--incremental`: the group must be added to the root in a file of its own, with nothing else changed, and the root must still plan with no changes.
- `TestAzureResume` imports again with `--resume` into a finished import's output, which must generate no root again.
- `TestAzureReuseInventory` imports with `--reuse-inventory` after discover: the log must say the import took the resources from the inventory discover saved. The listers reach floci-az over HTTPS, so unlike the Google Cloud test it doesn't count their requests.

CI runs the round trip, `TestAzureIncremental`, which adds to what the others import, and `TestAzureDrift`, which changes it, on runners of their own, and the other three on a fourth; run in one process, they share the resources the first one creates.

```sh
docker compose -f e2e/compose.yaml --profile azure up -d floci-az
curl -sf http://localhost:4577/_floci/tls-cert -o floci-az.crt   # then trust it, as CI does
E2E_AZURE_METADATA_HOST=localhost:4577 go test -tags e2e,minimal,azure -run TestAzure -v ./e2e/
docker compose -f e2e/compose.yaml --profile azure down
```

The test points the listers and the azurerm provider at the emulator as a custom cloud (`ARM_ENVIRONMENT=stack`, `ARM_METADATA_HOSTNAME`), signed in through its managed identity endpoint (`ARM_USE_MSI`, `ARM_MSI_ENDPOINT`): floci-az advertises an `http` issuer over HTTPS ([floci-az#255](https://github.com/floci-io/floci-az/issues/255)), which service principal sign-in rejects. Both read the cloud's endpoints over HTTPS, so the host must trust the CA floci-az generates; CI installs it into the system store. The test refuses any metadata host other than localhost. floci-az lists network resources by resource group only, hence `--resource-group`.

## AWS sandbox (a real account)

[`TestSandboxAWS`](sandbox_aws_test.go) runs against a real AWS account, for what the emulators can't show: VPCs with NAT gateways, an EC2 instance, a Lambda function, REST and HTTP APIs, WAF and CloudFront, resources in two regions, and the attributes AWS returns that Floci doesn't. The `sandbox-aws` workflow runs it, started by hand (`gh workflow run sandbox-aws -f engine=terraform`, the main estate by default), and only after a reviewer of the `sandbox` environment approves the run:

1. It signs in to two roles through GitHub's OpenID Connect, with no stored keys: `AWS_FIXTURE_ROLE_ARN` creates and destroys the estate, and `AWS_READONLY_ROLE_ARN`, the [read-only template](../docs/permissions.md#aws) (ReadOnlyAccess and the data-read deny), imports it, as a user would. Both are variables of the environment, with `AWS_REGION` and, optionally, `AWS_SECOND_REGION` (by default `eu-west-1`).
2. Terraform creates [`testdata/sandbox/aws`](testdata/sandbox/aws/main.tf) as the fixture role, every resource tagged `infraharvest-sandbox=<engine>` and `infraharvest-sandbox-run=<run id>` (the `run_id` variable, from `TF_VAR_run_id`): most of it in `AWS_REGION`, and a queue and a parameter in the second region, through a provider alias.
3. `infraharvest import aws --all --regions=<region>,<second region>` imports the resources of those services as the read-only role, and the generated configuration must plan as imports with no changes. Unlike on Floci, no attribute is excused as an emulator gap. Each region gets a root of its own with its own resources, and the global ones (IAM, Route 53, CloudFront) are imported once, into the global root. Both VPCs become calls of the vpc module: one with a single NAT gateway, one with a NAT gateway in each of two zones (`one_nat_gateway_per_az`).
4. The test interrupts that import, which goes one region at a time, as Ctrl-C would, when it starts on the second region's root, the last, and runs it again with `--resume` and both regions at once (`--parallel=2`): the log must show the two regions listing at the same time, the roots generated before the interrupt stay as they were, and the interrupted one comes out as it does when the test generates it again from nothing, one region at a time. So the output doesn't depend on `--parallel`, at no more calls to AWS than the resume makes anyway.
5. It then adds a log group to the estate (`-var extra=true`) and imports again with `--incremental`: only the log group is added, in a file of its own in its region's root, nothing else changes, and every root still plans with no changes, importing each resource once.
6. It applies the root the log group went to, in a copy of the output, keeping its state, as the read-only role: the apply only imports. It then gives the log group a `Name` tag (`-var extra_name=…`), which gives it another address, and imports again with `--regenerate`. The root's `moves.tf` must move the log group, and, planned over the state the first apply left, the root must show it as moved, creating and destroying nothing. . It gives the log group another `Name` tag out of band, as the fixture role, and runs `infraharvest drift --root=<that root>` on the applied copy as the read-only role: the tag must be reported as `kept`, `--detailed-exitcode` must exit with 4, and the code must stay as it was. With `--accept 'type=aws_cloudwatch_log_group'`, the tag must be written into the code, which must then plan no change. Only the log group changes in the cloud.
8. The estate is destroyed, by the test and again by the workflow, with fresh credentials, whatever happened; the workflow fails if anything is left in its state, which the log group is in too. Last, whatever happened, it sweeps the regions for what still carries the run's `infraharvest-sandbox-run` tag: it deletes the costly types (EC2 instances, NAT gateways and their Elastic IPs, RDS, Redshift, ElastiCache, EKS, load balancers, Kinesis streams, Lambda functions and S3 buckets) without final snapshots, and warns about the rest ([`sandbox-aws-sweep.sh`](../.github/scripts/sandbox-aws-sweep.sh)). It touches nothing without the run's tag, and covers the clusters and data estates too. The import's report (`report.md` and `coverage.json`) is uploaded as an artifact (`sandbox-aws-<estate>-<engine>`), kept for 7 days. The generated configuration is uploaded only encrypted (see [The generated configuration](#the-generated-configuration)), as the artifacts of a public repository are public.

The import covers the whole of both regions, so the account should hold nothing but the sandbox estate: anything else is imported and checked too, and the incremental import counts on nothing else changing while it runs. So only one run, of either engine and either estate, runs at a time. One run lasts under an hour and costs well under a dollar: three NAT gateways (about $0.045 an hour each, with their Elastic IPs), a load balancer and a `t4g.nano` instance.

### The clusters estate

RDS, ElastiCache and EKS take minutes to create and delete and cost by the hour, so they are an estate of their own, [`testdata/sandbox/aws-clusters`](testdata/sandbox/aws-clusters/main.tf), which a run creates only when asked to:

```sh
gh workflow run sandbox-aws -f estate=clusters -f engine=terraform
```

[`TestSandboxAWSClusters`](sandbox_aws_test.go) creates it in `AWS_REGION`, every resource that takes tags tagged `infraharvest-sandbox=<engine>`, as in the main estate: a VPC with two private subnets and no NAT gateway; a PostgreSQL `db.t4g.micro` instance (20 GB of gp3, one zone, no backups, its master password managed by RDS in Secrets Manager) with a subnet group and a parameter group of its own; a Valkey replication group of one `cache.t4g.micro` node with a subnet group and a parameter group; and an EKS control plane with no nodes and no add-ons, its role, and an access entry with a read-only access policy for a role of its own. EKS authenticates through access entries alone and makes none for the fixture role that creates the cluster, so the import finds only the estate's. The test imports the region (`--services=eks,elasticache,iam,rds,sg,subnet,vpc`) as the read-only role and checks, as the main test does, that nothing fails or is left out, every check passes, every root plans as imports with no changes and every type created is imported. It also checks that the import leaves out the security group EKS creates for the cluster. There is no resume or incremental import: the main estate covers those.

A run takes about an hour: about 15 minutes to create the estate (the cluster takes longest, ElastiCache and RDS create meanwhile), 15 to 20 to import and plan, and 10 to 15 to destroy; the job allows up to 190 minutes, and the test 120 (the `timeout-minutes` and `-timeout` of [`sandbox-aws.yml`](../.github/workflows/sandbox-aws.yml)). It costs about $0.15 an hour, under $0.25 a run: the EKS control plane ($0.10 an hour), the database and the cache node (about $0.02 an hour each), their storage and the secret.

### The data estate

[`testdata/sandbox/aws-data`](testdata/sandbox/aws-data/main.tf) holds what most AWS accounts have, the database and the warehouse included, which cost by the hour, so a run creates it only when asked to and destroys it as soon as the import is planned:

```sh
gh workflow run sandbox-aws -f estate=data -f engine=terraform
```

[`TestSandboxAWSData`](sandbox_aws_test.go) creates it in `AWS_REGION`, every resource tagged `infraharvest-sandbox=<engine>`: a VPC with two private subnets and no NAT gateway, and one security group; a `t4g.nano` EC2 instance with no public address; a Lambda function with its role and log group; an S3 bucket; a PostgreSQL `db.t4g.micro` instance (one zone, no backups, no final snapshot, its master password managed by RDS in Secrets Manager); and a single-node Redshift cluster (`ra3.large`, or the `redshift_node_type` variable; automated snapshots kept a day, which Redshift deletes with the cluster, and no final snapshot, its admin password managed by Redshift in Secrets Manager). The test imports the region (`--services=ec2_instance,iam,lambda,logs,rds,redshift,s3,sg,subnet,vpc`) as the read-only role and checks, as the main test does, that nothing fails or is left out, every check passes, every root plans as imports with no changes and every type created is imported. The workflow destroys the estate whatever happened, and allows the job 160 minutes and the test 90 (see [`sandbox-aws.yml`](../.github/workflows/sandbox-aws.yml)). RDS and Redshift take 5 to 15 minutes each to create and to delete, in parallel; a run costs well under $1.

## Azure sandbox (a real subscription)

[`TestSandboxAzure`](sandbox_azure_test.go) runs against a real Azure subscription, for what floci-az can't show: Resource Graph, which `--services='*'` lists through, route tables, storage accounts, Key Vault, management locks, resource groups' tags, and whether the [read-only template](../docs/permissions.md#azure) grants enough. The `sandbox-azure` workflow runs it, started by hand (`gh workflow run sandbox-azure -f engine=terraform`), and only after a reviewer of the `sandbox` environment approves the run:

1. It signs in as two user-assigned managed identities through GitHub's OpenID Connect (`ARM_USE_OIDC`), with no stored secrets: `AZURE_FIXTURE_CLIENT_ID` creates and destroys the estate, and `AZURE_READONLY_CLIENT_ID`, which has what [`permissions/azure`](../permissions/azure) grants (Reader on the subscription), imports it, as a user would. Both are variables of the environment, with `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` and `AZURE_RESOURCE_GROUP` (by default `infraharvest-sandbox`). A one-time bootstrap creates them (see [Setting up the Azure sandbox](#setting-up-the-azure-sandbox)).
2. Terraform creates [`testdata/sandbox/azure`](testdata/sandbox/azure/main.tf) in the estate's resource group as the fixture identity: a virtual network with two subnets, a network security group with a rule, a route table with a route, both associated with the subnets, a storage account with a container, a Key Vault in RBAC mode, a Log Analytics workspace, a user-assigned identity and a `CanNotDelete` lock on it. Every name starts `ih-sbx-<engine>-` (the storage account's `ihsbx<engine>`) and ends in a random suffix, and every resource that takes tags is tagged `infraharvest-sandbox=<engine>`.
3. `infraharvest import azure --services='*' --resource-group=<group> --all` imports the group as the read-only identity. Nothing may be denied: the test looks for authorisation errors in the import's log and its report. Nothing may fail or be left out, every check of the verification gate must pass, and the one root, the group's, must plan as imports with no changes. Each azurerm type the estate created must be imported at least as many times as it was created; the group, the route table, the storage account and its container, the Key Vault and the lock must be imported by their IDs; . It applies the root, in a copy of the output, as the read-only identity (the apply only imports), adds a `drift` tag to the network security group out of band (`-var drift_tag=…`), as the fixture identity, and runs `infraharvest drift` as the read-only identity: the tag must be reported as `kept`, `--detailed-exitcode` must exit with 4, and the code must stay as it was. With `--accept 'type=azurerm_network_security_group'`, the tag must be written into the code, which must then plan no change. Only the group changes in the cloud.
5. The estate is destroyed, by the test and again by the workflow, whatever happened; the workflow fails if anything is left in its state. When the run failed, the workflow also deletes the group's locks and what is left in it tagged with the run's engine. The import's report (`report.md` and `coverage.json`) is uploaded as an artifact (`sandbox-azure-<engine>`), kept for 7 days. The generated configuration is uploaded only encrypted (see [The generated configuration](#the-generated-configuration)), as the artifacts of a public repository are public.

The fixture identity is Contributor on the estate's resource group alone, with a custom role for its management locks, which Contributor can't manage: it can change nothing else in the subscription. Creating resource groups would take a role on the whole subscription, which Azure can't limit to names under a prefix, so the bootstrap creates the group, with its tags, and the estate lives in it; the group holds nothing between runs. For the same reason the fixture can't purge a deleted Key Vault, which belongs to the subscription: a run's vault stays soft-deleted for seven days, at no cost, under a name no other run uses. The read-only identity has Reader on the whole subscription, as the template grants, and the import keeps to the group, so the subscription may hold other things; one of its own is still best.

Only one run, of either engine, runs at a time. Nothing in the estate bills by the hour: the network, the route table, the identity and the lock are free, and the empty storage account, the unused Key Vault and the workspace with no data cost a fraction of a cent, so a run costs well under $0.01. It should take under half an hour; the job allows 120 minutes and the test 90 (see [`sandbox-azure.yml`](../.github/workflows/sandbox-azure.yml)).

### Setting up the Azure sandbox

[`testdata/sandbox/azure-bootstrap`](testdata/sandbox/azure-bootstrap/main.tf) creates what the workflow needs, once: the identities' resource group (`infraharvest-ci`) and the estate's (`infraharvest-sandbox`), the two identities with federated credentials that trust the `sandbox` environment's GitHub tokens, the fixture's roles on the estate's group, and the read-only identity's Reader role, through `permissions/azure` itself. It makes no app registration, so it needs no Entra ID role: an Owner of the subscription can apply it. In [Azure Cloud Shell](https://learn.microsoft.com/azure/cloud-shell/overview) (Bash), where Terraform and the Azure CLI are installed and signed in:

```sh
git clone https://github.com/IgnatG/infraharvest.git
cd infraharvest/e2e/testdata/sandbox/azure-bootstrap
az account set --subscription <subscription-id>
terraform init
terraform apply -var "subscription_id=$(az account show --query id -o tsv)"
terraform output -raw gh_commands
```

`-var location=<region>` places the groups elsewhere than `uksouth` (some regions, such as `westeurope`, refuse new subscriptions). The identities trust tokens whose subject is `repo:IgnatG@6107088/infraharvest@1420684118:environment:sandbox`, the repository's immutable subject for the environment; `-var github_subject=...` sets another. Keep the state, which Cloud Shell keeps in your home directory, to change or destroy it later.

The last command prints the `gh variable set` commands that set the environment's variables. Run them where `gh` is signed in as an administrator of the repository (in Cloud Shell, `gh auth login` first, then `terraform output -raw gh_commands | bash`), then start a run:

```sh
gh workflow run sandbox-azure -R IgnatG/infraharvest -f engine=terraform
```

## Google Cloud sandbox (a real project)

[`TestSandboxGoogle`](sandbox_google_test.go) runs against a real Google Cloud project, for what floci-gcp can't show: real Cloud Asset Inventory searches, which `cloud_asset` lists through, Cloud DNS zones and record sets, Cloud Routers, the project's own IAM policy, and whether the [read-only template](../docs/permissions.md#google-cloud) grants enough. The `sandbox-google` workflow runs it, started by hand (`gh workflow run sandbox-google -f engine=terraform`), and only after a reviewer of the `sandbox` environment approves the run:

1. It signs in as two service accounts through workload identity federation, which trades the job's GitHub OpenID Connect token for theirs, with no keys: `GOOGLE_FIXTURE_SERVICE_ACCOUNT` creates and destroys the estate, and `GOOGLE_READONLY_SERVICE_ACCOUNT`, which has what [`permissions/google`](../permissions/google) grants (Viewer, Security Reviewer and Cloud Asset Viewer on the project), imports it, as a user would. `google-github-actions/auth` writes a credentials file for each, and each step names the one it signs in with in `GOOGLE_APPLICATION_CREDENTIALS`. Both are variables of the environment, with `GOOGLE_PROJECT_ID`, `GOOGLE_REGION` (by default `europe-west2`) and `GOOGLE_WORKLOAD_IDENTITY_PROVIDER`. A one-time bootstrap creates them (see [Setting up the Google Cloud sandbox](#setting-up-the-google-cloud-sandbox)).
2. Terraform creates [`testdata/sandbox/google`](testdata/sandbox/google/main.tf) as the fixture account: a custom-mode VPC network with two subnetworks in the region, a firewall rule, a Cloud Router without NAT, a stopped (`TERMINATED`) `e2-micro` instance in the region's zone `a` with a 10 GB standard boot disk and a 10 GB standard data disk (the smallest a standard disk can be), with no external address or service account, a Cloud Storage bucket in the region, a Pub/Sub topic with a subscription, a private Cloud DNS zone with an `A` record set, a service account, and a 2nd gen Cloud Function (Python, 128 MiB, internal ingress only, no instances while idle) from a zip of inline code in the bucket, which Cloud Build builds as the bootstrap's `infraharvest-ci-function` account and which runs as it. Every name starts `ih-sbx-<engine>-` and ends in a random suffix, but the function's, `ih-sbx-function` for every run of either engine: Cloud Asset Inventory syncs functions only every 7 hours, so it still lists a function an earlier run deleted, which the import could only fail to import unless it is this run's. Every resource that takes labels is labelled `infraharvest-sandbox=<engine>`.
3. Cloud Asset Inventory's search lags behind changes by a few minutes, so the test searches the project once a minute, as the read-only account and for the estate's asset types alone, until it finds every resource of the estate it lists but the function, the instance's boot disk, and the Cloud Run service and `gcf-v2-sources-<project number>-<region>` bucket Cloud Functions creates for it, and no longer finds those an earlier run deleted, for up to ten minutes.
4. `infraharvest import google --services='*' --projects=<project> --regions=global,<region> --all` imports the project as the read-only account. Nothing may be denied: the test looks for permission errors in the import's log and its report. Nothing may fail or be left out, every check of the verification gate must pass, and the two roots, global's and the region's, must plan as imports with no changes. Each google type the estate created must be imported at least as many times as it was created; every resource of the estate must be imported by its ID, as must the project and the read-only account's three grants in the project's IAM policy; and the labels of each labelled resource must be in the configuration of its root. The default selection must leave out, each with its reason, the Cloud Run service the function runs on and the `gcf-v2-sources` bucket, and, as the boot disk of an instance, the instance's boot disk, which Google names after it, while the data disk is imported as a disk of its own and no disk is left out because the instances couldn't be listed; the function must be imported if the search lists it, and isn't required to be when Cloud Asset Inventory hasn't synced it yet, . It applies global's root, in a copy of the output, as the fixture account, as a team applies with its deployment identity (after an import the provider updates labels in place, to the same values), adds a `drift` label to the topic out of band (`-var drift_label=…`), as the fixture account, and runs `infraharvest drift --root=<global's root>` as the read-only account: the label must be reported as `kept`, `--detailed-exitcode` must exit with 4, and the code must stay as it was. With `--accept 'type=google_pubsub_topic'`, the label must be written into the code, which must then plan no change but to the labels the provider keeps only in state. Only the topic changes in the cloud.
6. The estate is destroyed, by the test and again by the workflow, whatever happened; the workflow fails if anything is left in its state. When the run failed, the workflow also deletes, as the fixture account, what is left named after the run's engine, and the function, `ih-sbx-function`: networks, subnetworks, firewall rules, routers and service accounts take no labels, so the sweep goes by name. The import's report (`report.md` and `coverage.json`) is uploaded as an artifact (`sandbox-google-<engine>`), kept for 7 days. The generated configuration is uploaded only encrypted (see [The generated configuration](#the-generated-configuration)), as the artifacts of a public repository are public.

The fixture account has the predefined roles that manage the estate's kinds of resources: Cloud Functions Developer, with Service Account User on `infraharvest-ci-function` alone, to deploy the function as it (Cloud Functions creates and deletes the Cloud Run service itself), Compute Network Admin and Compute Security Admin (networks, subnetworks, routers and firewall rules), Compute Instance Admin (v1) (the instance and its disks), DNS Administrator, Pub/Sub Editor, Storage Admin, and Service Account Creator, Deleter and Viewer rather than Service Account Admin, so that it can neither create keys nor change who may use a service account. It can't change the project's IAM policy. Google Cloud can't limit a role to names under a prefix, so these hold for the whole project: the fixture could change or delete other networks, buckets, topics, zones and service accounts in it, the bootstrap's own service accounts too. The import covers the whole project as well, and anything else in it is imported and checked. So the sandbox needs a project of its own, which holds nothing else, and only one run, of either engine, runs at a time.

Nothing in the estate bills by the hour but the instance's two standard disks, about $1 a month for both, prorated over the minutes they exist: the stopped instance bills nothing (it runs for the seconds before it stops), and the network, the subnetworks, the firewall rule and the router (with no NAT) are free, and so are a bucket that holds the function's few hundred bytes of source, a topic and a subscription with no messages, a function with no instances that is never called, and the searches; the function's build takes a minute or two of Cloud Build's free tier; the private zone costs $0.20 a month, prorated over the minutes it exists, and answers no queries. A run costs well under $0.01. It should take under half an hour; the job allows 120 minutes and the test 90 (see [`sandbox-google.yml`](../.github/workflows/sandbox-google.yml)).

Destroying the function deletes the Cloud Run service with it, but Cloud Functions keeps what it creates in the project once per region: the `gcf-v2-sources-<project number>-<region>` bucket, with a copy of each run's source (a few hundred bytes), and the `gcf-artifacts` Artifact Registry repository, with the images Cloud Build pushed (tens of megabytes each, within Artifact Registry's free 0.5 GB for a while). Later runs use them, and the default selection leaves both out. When you are done with the sandbox, or the images add up, delete them as an Owner: `gcloud artifacts repositories delete gcf-artifacts --location <region>` and `gcloud storage rm --recursive gs://gcf-v2-sources-<project number>-<region>`. Cloud Build's logs stay in Cloud Logging, for its default retention.

### Setting up the Google Cloud sandbox

[`testdata/sandbox/google-bootstrap`](testdata/sandbox/google-bootstrap/main.tf) creates what the workflow needs, once: it enables the APIs the estate, the import and the sign-in use (left enabled if the bootstrap is destroyed), creates a workload identity pool (`infraharvest-github`) with a provider that accepts only the GitHub tokens of this repository's `sandbox` environment, by the repository's and its owner's immutable IDs, the two service accounts, `infraharvest-ci-fixture` and `infraharvest-ci-readonly`, which the environment's jobs may impersonate, the fixture's roles, and the read-only account's roles, through `permissions/google` itself. It also creates `infraharvest-ci-function`, which nobody impersonates: Cloud Build builds the estate's function as it, with Artifact Registry Writer, Logs Writer and Storage Object Viewer, as Google's docs have a custom build service account, since a new project's default Cloud Build service account may lack them, and the function runs as it. It creates no service account key.

It needs a project of its own with billing enabled (Compute Engine and Cloud DNS need it), the Service Usage API enabled, as it is in new projects, and you an Owner of the project. Organization policies can get in the way: `iam.workloadIdentityPoolProviders` must allow the issuer `https://token.actions.githubusercontent.com`, domain restricted sharing (`iam.allowedPolicyMemberDomains`) can refuse the grants to the pool's `principal://` identity, and `gcp.resourceLocations` must allow the region. A policy against service account keys doesn't matter. In [Google Cloud Shell](https://cloud.google.com/shell/docs), where Terraform and the gcloud CLI are installed and signed in:

```sh
git clone https://github.com/IgnatG/infraharvest.git
cd infraharvest/e2e/testdata/sandbox/google-bootstrap
gcloud config set project <project-id>
gcloud services enable cloudresourcemanager.googleapis.com serviceusage.googleapis.com
terraform init
terraform apply -var "project_id=$(gcloud config get-value project)"
terraform output -raw gh_commands
```

`-var region=<region>` creates the estate elsewhere than `europe-west2` (London). The service accounts trust tokens whose subject is `repo:IgnatG@6107088/infraharvest@1420684118:environment:sandbox`, the repository's immutable subject for the environment; `-var github_subject=...`, `-var github_repository_id=...` and `-var github_repository_owner_id=...` set others. A deleted pool keeps its ID for 30 days, so to create it again sooner, `-var pool_id=...` names another. Keep the state, which Cloud Shell keeps in your home directory (5 GB that persist between sessions), to change or destroy it later; Cloud Shell deletes a home directory that goes unused for 120 days.

The last command prints the `gh variable set` commands that set the environment's variables. Run them where `gh` is signed in as an administrator of the repository (in Cloud Shell, `gh auth login` first, then `terraform output -raw gh_commands | bash`), then start a run:

```sh
gh workflow run sandbox-google -R IgnatG/infraharvest -f engine=terraform
```

When the bootstrap changes, such as when the estate gains a kind of resource that needs another API or role, apply it again from the state you kept, in the same directory and as an Owner, before the next run: `git pull`, then `gcloud config set project <project-id>` (a new Cloud Shell session has no project set, and `gcloud config get-value project` would then give an empty `project_id`), then `terraform apply -var "project_id=$(gcloud config get-value project)"`, with any `-var` you set the first time. The variables of the environment change only if `terraform output -raw gh_commands` prints other values.

## Applying the generated roots

Each sandbox test also does what a team does with the output. It copies the output directory without any state, then for each generated root:

1. Runs `terraform init`.
2. Runs `terraform apply` as the read-only identity that imported. The apply only imports: every resource goes into a new local state, and nothing in the cloud changes, so read access is all it needs. Google Cloud is the exception: after an import, the Google provider updates the labels of each labelled resource in place, to the values it already has, which needs the right to update it. So the Google test applies as the fixture account, as a team applies with its deployment identity, and the bootstrap gives the fixture `roles/viewer` and `roles/iam.securityReviewer` to read what the roots import.
3. Runs `terraform plan` again, which must find nothing to import or change.

The test fails if the apply leaves nothing in state, or if anything is still planned after it. The state stays on the runner and goes with it.

## The generated configuration

When the repository variable `SANDBOX_OUTPUT_KEY` holds an OpenPGP public key, each sandbox run encrypts its output directory to that key and uploads it as the artifact `sandbox-<cloud>-...-output`, kept for 7 days. The archive leaves out `.terraform` directories, state and variables files. Only the holder of the private key can read it:

```sh
gh run download <run-id> -n sandbox-aws-main-terraform-output
gpg --decrypt output.tar.gz.gpg | tar -xz -C <directory>
```

Without the variable, only the report is kept.

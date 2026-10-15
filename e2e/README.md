# End-to-end tests

These tests run infraharvest against local cloud emulators, so they need no cloud account and cost nothing. CI runs them on every pull request, except the sandbox tests, which run against a real AWS account and only by hand (see [AWS sandbox](#aws-sandbox-a-real-account)).

## AWS (Floci)

[`TestAWSRoundTrip`](aws_test.go) uses [Floci](https://github.com/floci-io/floci), an open-source AWS emulator:

1. Terraform creates the resources in [`testdata/aws`](testdata/aws/main.tf) in Floci.
2. `infraharvest import aws --all` imports them. A second run imports again from the selection file `infraharvest discover aws` writes, and must write the same files byte for byte.
3. Terraform plans the generated configuration, with the secret variables set to the values from step 1. Every resource must be an import with no changes, no resource may be left out (`rejected.hcl`), and every resource type created in step 1 must be imported.

Run it locally (needs Docker and Terraform >= 1.5; compiling uses a few GB of RAM):

```sh
docker compose -f e2e/compose.yaml up -d --wait
AWS_ENDPOINT_URL=http://localhost:4566 go test -tags e2e,minimal,aws -v ./e2e/
docker compose -f e2e/compose.yaml down
```

Set `E2E_ENGINE=tofu` to run the same test with OpenTofu >= 1.6 (`--engine=tofu`). CI runs it with both engines, because the output must work on both.

The test doesn't delete what it creates, so restart Floci (`down`, then `up`) before running it again.

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

```sh
docker compose -f e2e/compose.yaml --profile google up -d floci-gcp
INFRAHARVEST_GOOGLE_ENDPOINT=http://localhost:4588 go test -tags e2e,minimal,google -run TestGoogle -v ./e2e/
docker compose -f e2e/compose.yaml --profile google down
```

`INFRAHARVEST_GOOGLE_ENDPOINT` sends the listers' Google API calls to the emulator, without credentials. The test points the Terraform provider there through its `GOOGLE_*_CUSTOM_ENDPOINT` variables, with a placeholder access token. It refuses any endpoint other than localhost.

floci-gcp doesn't serve Cloud Asset Inventory or Cloud DNS. A stand-in in front of it answers `cloud_asset`'s searches from what step 1 created, as Cloud Asset Inventory names it, with the default network and one of its firewall rules, which the default selection must leave out, and finds no DNS zones; floci-gcp serves the rest, such as the IAM policy, the Cloud SQL and Cloud Scheduler lists, and what Terraform plans with. Not covered yet: DNS record sets, Cloud SQL databases and Cloud Scheduler jobs, which only unit tests cover, and the shapes of real Cloud Asset Inventory results, which the stand-in only imitates.

## Azure (floci-az)

[`TestAzureRoundTrip`](azure_test.go) uses [floci-az](https://github.com/floci-io/floci-az), the Azure member of the same emulator family:

1. Terraform creates the resources in [`testdata/azure`](testdata/azure/main.tf) in floci-az: a resource group with a virtual network and subnet, a network security group with a rule, a public IP, and a network interface.
2. `infraharvest import azure --all --resource-group=infraharvest-e2e` imports them. `--services` names the services that list through Resource Manager, since floci-az has no Resource Graph, which `--services=*` uses.
3. Terraform plans the generated configuration. Every resource must be an import with no changes, and every resource type created in step 1 must be imported.

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
2. Terraform creates [`testdata/sandbox/aws`](testdata/sandbox/aws/main.tf) as the fixture role, every resource tagged `infraharvest-sandbox=<engine>`: most of it in `AWS_REGION`, and a queue and a parameter in the second region, through a provider alias.
3. `infraharvest import aws --all --regions=<region>,<second region>` imports the resources of those services as the read-only role, and the generated configuration must plan as imports with no changes. Unlike on Floci, no attribute is excused as an emulator gap. Each region gets a root of its own with its own resources, and the global ones (IAM, Route 53, CloudFront) are imported once, into the global root. Both VPCs become calls of the vpc module: one with a single NAT gateway, one with a NAT gateway in each of two zones (`one_nat_gateway_per_az`).
4. The test interrupts that import, as Ctrl-C would, when it starts on the second region's root, the last, and runs it again with `--resume`: the roots generated before the interrupt stay as they were, and the interrupted one comes out as it does when the test generates it again from nothing.
5. It then adds a log group to the estate (`-var extra=true`) and imports again with `--incremental`: only the log group is added, in a file of its own in its region's root, nothing else changes, and every root still plans with no changes, importing each resource once.
6. The estate is destroyed, by the test and again by the workflow, with fresh credentials, whatever happened; the workflow fails if anything is left in its state, which the log group is in too. The import is uploaded as an artifact (`sandbox-aws-<estate>-<engine>`), without `.terraform`.

The import covers the whole of both regions, so the account should hold nothing but the sandbox estate: anything else is imported and checked too, and the incremental import counts on nothing else changing while it runs. So only one run, of either engine and either estate, runs at a time. One run lasts under an hour and costs well under a dollar: three NAT gateways (about $0.045 an hour each, with their Elastic IPs), a load balancer and a `t4g.nano` instance.

### The clusters estate

RDS, ElastiCache and EKS take minutes to create and delete and cost by the hour, so they are an estate of their own, [`testdata/sandbox/aws-clusters`](testdata/sandbox/aws-clusters/main.tf), which a run creates only when asked to:

```sh
gh workflow run sandbox-aws -f estate=clusters -f engine=terraform
```

[`TestSandboxAWSClusters`](sandbox_aws_test.go) creates it in `AWS_REGION`, every resource that takes tags tagged `infraharvest-sandbox=<engine>`, as in the main estate: a VPC with two private subnets and no NAT gateway; a PostgreSQL `db.t4g.micro` instance (20 GB of gp3, one zone, no backups, its master password managed by RDS in Secrets Manager) with a subnet group and a parameter group of its own; a Valkey replication group of one `cache.t4g.micro` node with a subnet group and a parameter group; and an EKS control plane with no nodes and no add-ons, its role, and an access entry with a read-only access policy for a role of its own. EKS authenticates through access entries alone and makes none for the fixture role that creates the cluster, so the import finds only the estate's. The test imports the region (`--services=eks,elasticache,iam,rds,sg,subnet,vpc`) as the read-only role and checks, as the main test does, that nothing fails or is left out, every check passes, every root plans as imports with no changes and every type created is imported. It also checks that the import leaves out the security group EKS creates for the cluster. There is no resume or incremental import: the main estate covers those.

A run takes about an hour: about 15 minutes to create the estate (the cluster takes longest, ElastiCache and RDS create meanwhile), 15 to 20 to import and plan, and 10 to 15 to destroy; the job allows up to 160 minutes. It costs about $0.15 an hour, under $0.25 a run: the EKS control plane ($0.10 an hour), the database and the cache node (about $0.02 an hour each), their storage and the secret.

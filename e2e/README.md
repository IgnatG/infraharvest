# End-to-end tests

These tests run infraharvest against local cloud emulators, so they need no cloud account and cost nothing. CI runs them on every pull request.

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

## GCP (floci-gcp)

[`TestGCPRoundTrip`](gcp_test.go) uses [floci-gcp](https://github.com/floci-io/floci-gcp), the GCP member of the same emulator family:

1. Terraform creates the resources in [`testdata/gcp`](testdata/gcp/main.tf) in floci-gcp: a VPC network with a subnetwork and a firewall rule, a Cloud Storage bucket, and a Pub/Sub topic with a subscription.
2. `infraharvest import google --all` imports them.
3. Terraform plans the generated configuration. Every resource must be an import with no changes, and every resource type created in step 1 must be imported.

```sh
docker compose -f e2e/compose.yaml --profile gcp up -d floci-gcp
INFRAHARVEST_GCP_ENDPOINT=http://localhost:4588 go test -tags e2e,minimal,google -run TestGCP -v ./e2e/
docker compose -f e2e/compose.yaml --profile gcp down
```

`INFRAHARVEST_GCP_ENDPOINT` sends the listers' Google API calls to the emulator, without credentials. The test points the Terraform provider there through its `GOOGLE_*_CUSTOM_ENDPOINT` variables, with a placeholder access token. It refuses any endpoint other than localhost.

Not covered yet: the listers that use gRPC clients (IAM, Cloud Tasks, Cloud Build, Logging). floci-gcp serves IAM over REST only.

## Azure (floci-az)

[`TestAzureRoundTrip`](azure_test.go) uses [floci-az](https://github.com/floci-io/floci-az), the Azure member of the same emulator family:

1. Terraform creates the resources in [`testdata/azure`](testdata/azure/main.tf) in floci-az: a resource group with a virtual network and subnet, a network security group with a rule, a public IP, and a network interface.
2. `infraharvest import azure --all --resource-group=infraharvest-e2e` imports them.
3. Terraform plans the generated configuration. Every resource must be an import with no changes, and every resource type created in step 1 must be imported.

```sh
docker compose -f e2e/compose.yaml --profile azure up -d floci-az
curl -sf http://localhost:4577/_floci/tls-cert -o floci-az.crt   # then trust it, as CI does
E2E_AZURE_METADATA_HOST=localhost:4577 go test -tags e2e,minimal,azure -run TestAzure -v ./e2e/
docker compose -f e2e/compose.yaml --profile azure down
```

The test points the listers and the azurerm provider at the emulator as a custom cloud (`ARM_ENVIRONMENT=stack`, `ARM_METADATA_HOSTNAME`), with a placeholder service principal. Both read the cloud's endpoints over HTTPS, so the host must trust the CA floci-az generates; CI installs it into the system store. The test refuses any metadata host other than localhost. floci-az lists network resources by resource group only, hence `--resource-group`.

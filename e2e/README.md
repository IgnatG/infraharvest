# End-to-end tests

These tests run infraharvest against local cloud emulators, so they need no cloud account and cost nothing. CI runs them on every pull request.

## AWS (Floci)

[`TestAWSRoundTrip`](aws_test.go) uses [Floci](https://github.com/floci-io/floci), an open-source AWS emulator:

1. Terraform creates the resources in [`testdata/aws`](testdata/aws/main.tf) in Floci.
2. `infraharvest import aws --engine=terraform` imports them.
3. Terraform plans the generated configuration. Every resource must be an import with no changes, and every resource type created in step 1 must be imported.

Run it locally (needs Docker and Terraform >= 1.5; compiling uses a few GB of RAM):

```sh
docker compose -f e2e/compose.yaml up -d --wait
AWS_ENDPOINT_URL=http://localhost:4566 go test -tags e2e,minimal,aws -v ./e2e/
docker compose -f e2e/compose.yaml down
```

The test doesn't delete what it creates, so restart Floci (`down`, then `up`) before running it again.

The test uses test credentials and empty AWS config files, and refuses any `AWS_ENDPOINT_URL` other than localhost, so it never touches a real account.

To cover another service, add its resources to `testdata/aws/main.tf` and its infraharvest service name to `awsServices` in `aws_test.go`. Check Floci [supports the service](https://github.com/floci-io/floci/tree/main/docs/services) first.

An emulator doesn't enforce IAM permissions or reproduce every AWS API quirk, so this doesn't replace a check against a real account.

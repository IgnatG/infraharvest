# Read-only permissions

infraharvest only reads: it lists resources and lets Terraform import their configuration. It never creates, changes or deletes anything. These Terraform configurations grant the minimum access for that. Apply the one for your cloud once, with an identity that can manage IAM, then run infraharvest as the principal they grant.

> These templates are being validated against real accounts. If an import fails with an access-denied error, please open an issue with the resource type.

## AWS

Creates the `infraharvest-readonly` role:

- **Trust:** the principals you list may assume it.
- **`ReadOnlyAccess`:** the AWS-managed policy, which reads every service's configuration.
- **[`deny-data-reads.json`](aws/deny-data-reads.json):** an explicit deny on reading data. That covers S3 object contents, DynamoDB items, Kinesis records, SQS messages, Secrets Manager values, KMS decryption, CloudWatch Logs events and queries, Athena results, EC2 Windows passwords, ECR image layers and CodeCommit file contents.

```sh
cd permissions/aws
terraform init
terraform apply -var 'trusted_principal_arns=["arn:aws:iam::123456789012:role/platform-ci"]'
```

Then run infraharvest with a profile that assumes the role (`role_arn = <role_arn output>` in `~/.aws/config`).

Trade-off: `kms:Decrypt` is denied, so SSM `SecureString` parameters fail to import instead of having their decrypted values written into the generated code.

Without Terraform, attach `ReadOnlyAccess` and `deny-data-reads.json` (as an inline policy) to any role or user.

## Azure

Assigns the built-in **Reader** role to a user, group, service principal or managed identity. The scope is a subscription by default, or a management group. Reader has no data actions, so it can't read blob contents, Key Vault secrets or database rows.

```sh
cd permissions/azure
terraform init
terraform apply -var subscription_id=<subscription-guid> -var principal_id=<object-id>
# a management group instead:
#   -var scope=/providers/Microsoft.Management/managementGroups/<name>
```

Known gap: some storage account settings are read through the data plane, which Reader can't access. Those attributes may be missing until this is validated.

## Google Cloud

Grants **`roles/viewer`** (resource configuration) and **`roles/iam.securityReviewer`** (IAM policies on resources) on a project.

```sh
cd permissions/google
terraform init
terraform apply -var project_id=<project> -var member=serviceAccount:<email>
```

For a folder or organisation, grant the same two roles there with `google_folder_iam_member` or `google_organization_iam_member`.

Known gap: `roles/viewer` can also read some data, for example object listings. A custom role limited to metadata will replace it once the exact permissions are confirmed.

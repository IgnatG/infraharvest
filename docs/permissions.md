# Permissions

infraharvest only reads: it lists resources and lets Terraform import their configuration. It never creates, changes or deletes anything. The Terraform configurations in [`permissions/`](https://github.com/IgnatG/infraharvest/tree/main/permissions) grant the minimum access for that. Apply the one for your cloud once, with an identity that can manage IAM, then run infraharvest as the principal they grant.

> The templates are tested against a real AWS account, Azure subscription and Google Cloud project (the `sandbox-aws`, `sandbox-azure` and `sandbox-google` workflows). If an import fails with an access-denied error, please open an issue with the resource type.

## AWS

Creates the `infraharvest-readonly` role:

- **Trust:** the principals you list may assume it.
- **`ReadOnlyAccess`:** the AWS-managed policy, which reads every service's configuration.
- **[`deny-data-reads.json`](https://github.com/IgnatG/infraharvest/blob/main/permissions/aws/deny-data-reads.json):** an explicit deny on reading data. That covers S3 object contents, DynamoDB items, Kinesis records, SQS messages, Secrets Manager values, KMS decryption, CloudWatch Logs events and queries, Athena results, EC2 Windows passwords, ECR image layers and CodeCommit file contents.

```sh
cd permissions/aws
terraform init
terraform apply -var 'trusted_principal_arns=["arn:aws:iam::123456789012:role/platform-ci"]'
```

Then run infraharvest with a profile that assumes the role (`role_arn = <role_arn output>` in `~/.aws/config`).

`ReadOnlyAccess` also lets infraharvest read Resource Explorer's index and search it, for `--resource-explorer` (see [Resource Explorer](aws.md#resource-explorer)).

Trade-off: `kms:Decrypt` is denied, so SSM `SecureString` parameters fail to import instead of having their decrypted values written into the generated code.

Without Terraform, attach `ReadOnlyAccess` and `deny-data-reads.json` (as an inline policy) to any role or user.

## Azure

Assigns the built-in **Reader** role to a user, group, service principal or managed identity. The scope is a subscription by default, or a management group. Reader has no data actions, so it can't read blob contents, Key Vault secrets or database rows. Its `*/read` covers what every service reads, such as the role assignments and role definitions `role_assignment` lists (`Microsoft.Authorization/roleAssignments/read`, `Microsoft.Authorization/roleDefinitions/read`), which Resource Graph shows it, and which the generated roots' plans read again, and `Microsoft.Insights/diagnosticSettings/read` for the opt-in `diagnostic_setting`.

```sh
cd permissions/azure
terraform init
terraform apply -var subscription_id=<subscription-guid> -var principal_id=<object-id>
# a management group instead:
#   -var scope=/providers/Microsoft.Management/managementGroups/<name>
```

Storage accounts' queue and table service settings (logging, metrics, CORS) are read through the storage data plane, with the account's keys or a data role, which Reader doesn't grant. So the default selection leaves out `azurerm_storage_account_queue_properties` and `azurerm_storage_account_table_properties`; azurerm 5, which the generated roots use, doesn't read them as part of `azurerm_storage_account` either. To import them, select them and run with access to the account's keys (such as Storage Account Contributor) or a storage data role.

## Google Cloud

Grants **`roles/viewer`** (resource configuration), **`roles/iam.securityReviewer`** (IAM policies on resources) and **`roles/cloudasset.viewer`** (the Cloud Asset Inventory search most of a project is listed through) on a project. The search also needs the Cloud Asset API enabled in the project your credentials' quota goes to: `gcloud services enable cloudasset.googleapis.com --project=<project>`.

```sh
cd permissions/google
terraform init
terraform apply -var project_id=<project> -var member=serviceAccount:<email>
```

For a folder or organisation, grant the same three roles there with `google_folder_iam_member` or `google_organization_iam_member`.

Known gap: `roles/viewer` can also read some data, for example object listings. A custom role limited to metadata will replace it once the exact permissions are confirmed.

## Drift: reading the state

`infraharvest drift` refreshes roots that are applied, so besides the access above it needs read access to their state (see [Drift](drift.md#credentials)). None of the roles above grant it: the AWS role denies `s3:GetObject` and `kms:Decrypt`, `Reader` has no data actions, and `roles/viewer` doesn't read objects. The drift templates grant it on the state alone, with the read-only access above. Drift never takes the state's lock, so none of them grants write access for it. Applying the saved refresh plan to drift's private copy configures the provider again: with AWS, that calls `sts:GetCallerIdentity`, which needs no permission, and with Azure and Google Cloud signs in again, so the credentials must still be valid after the refresh (see the GitHub Action's [Permissions for drift](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#permissions-for-drift)).

### AWS: `permissions/aws-drift`

Creates the `infraharvest-drift` role, for the S3 backend:

- **Trust** and **`ReadOnlyAccess`:** as the read-only role.
- **`read-state`:** `s3:ListBucket` on the state's bucket and `s3:GetObject` on the roots' state keys, `<key_prefix>/<root path>/terraform.tfstate`, and `kms:Decrypt` on the state's key when the backend sets `kms_key_id`.
- **`deny-data-reads`:** the read-only role's [`deny-data-reads.json`](https://github.com/IgnatG/infraharvest/blob/main/permissions/aws/deny-data-reads.json), read from `permissions/aws`, with the object reads (and `kms:Decrypt`, with a state key) denied everywhere but the state instead.

```sh
cd permissions/aws-drift
terraform init
terraform apply \
  -var 'trusted_principal_arns=["arn:aws:iam::123456789012:role/platform-ci"]' \
  -var state_bucket=acme-terraform-state -var state_key_prefix=imported
# with an encrypted state:
#   -var state_kms_key_arn=arn:aws:kms:eu-west-2:123456789012:key/<id>
```

Then run `infraharvest drift` with a profile that assumes the role. The state holds secrets, which is why this is a role of its own: the import's role never reads it.

### Azure: `permissions/azure-drift`

Assigns **Reader**, as `permissions/azure` does, and **Storage Blob Data Reader** on the state's container only. Run drift with `ARM_USE_AZUREAD=true`, so that the backend reads the state with the identity's token rather than with the storage account's access key, which opens the whole account.

```sh
cd permissions/azure-drift
terraform init
terraform apply -var subscription_id=<subscription-guid> -var principal_id=<object-id> \
  -var state_storage_account_id=/subscriptions/<id>/resourceGroups/<group>/providers/Microsoft.Storage/storageAccounts/<account> \
  -var state_container_name=<container>
# when permissions/azure assigns Reader to the principal already:
#   -var assign_reader=false
```

### Google Cloud: `permissions/google-drift`

Grants the three roles `permissions/google` grants on the project, and **Storage Object Viewer** on the state's bucket only: `storage.objects.get`, to read `<prefix>/<root path>/default.tfstate`, and `storage.objects.list`, which the backend uses to find the workspaces.

```sh
cd permissions/google-drift
terraform init
terraform apply -var project_id=<project> -var member=serviceAccount:<email> -var state_bucket=acme-terraform-state
# when permissions/google grants the member the read-only roles already:
#   -var grant_read_only=false
```

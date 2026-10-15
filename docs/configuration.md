# Configuration

`--config infraharvest.yaml` sets any flag the command line doesn't, and the state backend of the generated roots:

```yaml
version: 1
settings:                # flags of every provider command
  selection: selection.yaml
providers:               # flags of one provider command
  aws:
    profile: prod
    regions: [eu-west-2, us-east-1]
    services: [vpc, subnet, sg, s3]
backend:                 # one of s3, azurerm, gcs
  s3:
    bucket: acme-terraform-state
    region: eu-west-2
    key_prefix: imported
modules:                 # your own modules, before the curated ones (see Modules)
  - source: git::https://github.com/acme/terraform-modules.git//bucket?ref=v1.4.0
```

A setting is named after its flag, without the dashes. A list gives a flag several values: comma-separated for `regions` and `services`, and one filter per item for `id`, `tag` and `where`, which the command line takes once per filter, as in `id: ["vpc=vpc-1,vpc-2", subnet=subnet-1]`.

Each root gets a `backend.tf` with its own state key (`imported/aws/<account>/<region>/terraform.tfstate`). S3 state is locked with S3's own lock file (`use_lockfile = true`), not DynamoDB. infraharvest writes `backend.tf` after checking the root, so importing never needs access to the state bucket.

If the storage doesn't exist yet, `infraharvest bootstrap --config infraharvest.yaml` writes a root into `<out>/bootstrap` that creates it: an S3 bucket, an Azure storage account and container, or a Cloud Storage bucket. The storage is versioned, encrypted, not public, reachable only over TLS, and protected from `terraform destroy`. Apply it once, with rights to create storage, before planning the generated roots.

## Behind a TLS-intercepting proxy

If a proxy re-signs HTTPS traffic with its own certificate authority, point `SSL_CERT_FILE` at a PEM file of the certificates to trust (that authority's and the public ones), or `SSL_CERT_DIR` at a directory of them. infraharvest then trusts those certificates instead of the system's, on Windows and macOS as well as on Linux. Terraform and the providers are separate programs that may not read these variables on Windows and macOS, so there also add the authority to the system's trust store.

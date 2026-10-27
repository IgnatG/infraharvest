# Import report

infraharvest v0.0.0-20261027134858-e08be018a826, terraform 1.16.6, provider `registry.terraform.io/hashicorp/aws` ~> 6.70 (6.70.0).

| Discovered | Imported | Excluded | Left out | Not importable | Failed |
|---|---|---|---|---|---|
| 1 | 1 | 0 | 0 | 0 | 0 |

## By type

| Type | Discovered | Imported | Excluded | Left out | Not importable | Failed |
|---|---|---|---|---|---|---|
| `aws_sqs_queue` | 1 | 1 | 0 | 0 | 0 | 0 |

## Managed and unmanaged

What Terraform already manages, according to the state read with --managed-state or to the resources' tags, what other tools such as CloudFormation or Pulumi manage, and what the cloud manages itself, such as a default VPC, or the default selection leaves out for another reason: the import left those out (--include-owned imports those their tags say are managed). The rest isn't under infrastructure as code yet.

| Type | Discovered | Managed by Terraform | Managed by another tool | Managed by the cloud | Not managed |
|---|---|---|---|---|---|
| `aws_sqs_queue` | 1 | 0 | 0 | 0 | 1 |

## Skipped checks

These checks couldn't run on these directories, so they checked nothing. They don't fail the import.

- aws/000000000000/us-east-1: G5 scanners: no scanner installed (tflint, trivy, checkov)

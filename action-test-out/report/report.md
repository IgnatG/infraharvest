# Import report

infraharvest v0.0.0-20261026074054-eb77d91e90ee, terraform 1.16.6, provider `registry.terraform.io/hashicorp/aws` ~> 6.70 (6.70.0).

| Discovered | Imported | Excluded | Left out | Not importable | Failed |
|---|---|---|---|---|---|
| 1 | 1 | 0 | 0 | 0 | 0 |

## By type

| Type | Discovered | Imported | Excluded | Left out | Not importable | Failed |
|---|---|---|---|---|---|---|
| `aws_sqs_queue` | 1 | 1 | 0 | 0 | 0 | 0 |

## Skipped checks

These checks couldn't run on these directories, so they checked nothing. They don't fail the import.

- aws/000000000000/us-east-1: G5 scanners: no scanner installed (tflint, trivy, checkov)

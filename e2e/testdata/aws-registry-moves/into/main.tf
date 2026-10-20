# A root that moves a bucket into a call of a module from the registry,
# the curated s3-bucket module at the release its adapter pins, as a root
# generated again would if its bucket became a call of the module
# (moves_registry_test.go). Terraform rejects the moved block, as moved
# blocks stay within a module package. The endpoint, region and
# credentials come from the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

module "assets" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "5.17.0"

  bucket = "infraharvest-e2e-registry-moves"
}

moved {
  from = aws_s3_bucket.assets
  to   = module.assets.aws_s3_bucket.this[0]
}

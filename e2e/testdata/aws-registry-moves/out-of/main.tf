# A root that moves a bucket out of a call of a module from the registry,
# the curated s3-bucket module at the release its adapter pins, into a
# resource of the root, as a root generated again would if its bucket were
# no longer a call of the module (moves_registry_test.go). The call stays,
# without the bucket. Terraform and OpenTofu accept the moved block: the
# call is the root's own. The endpoint, region and
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

  create_bucket = false
  bucket        = "infraharvest-e2e-registry-moves"
}

resource "aws_s3_bucket" "assets" {
  bucket = "infraharvest-e2e-registry-moves-assets"
}

moved {
  from = module.assets.aws_s3_bucket.this[0]
  to   = aws_s3_bucket.assets
}

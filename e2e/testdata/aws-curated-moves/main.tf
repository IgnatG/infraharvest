# Resources the end-to-end test of --regenerate's moves into and out of
# calls of curated modules from the registry (moves_modules_test.go)
# creates in an account of the emulator of its own, which takes a 12-digit
# access key ID as the account. The endpoint and region come from the
# environment the test sets. Two buckets, each with its versioning, which
# --modules=registry makes calls of the curated s3-bucket module, and
# --modules=none keeps in the root.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = "989898989898"
  secret_key = "test"
}

resource "aws_s3_bucket" "assets" {
  bucket = "infraharvest-e2e-curated-moves-assets"
}

resource "aws_s3_bucket_versioning" "assets" {
  bucket = aws_s3_bucket.assets.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket" "backups" {
  bucket = "infraharvest-e2e-curated-moves-backups"
}

resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

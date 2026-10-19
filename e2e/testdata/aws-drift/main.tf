# Resources the end-to-end test of infraharvest drift (drift_test.go)
# creates in an account of the emulator of its own, which takes a 12-digit
# access key ID as the account. The endpoint and region come from the
# environment the test sets. Applied again with other variables, it
# changes the resources out of band of the roots the import generated:
# a queue's tag and retention, the tag every resource shares, and the tag
# of a bucket, which the import makes a call of the s3-bucket module.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = "666666666666"
  secret_key = "test"
}

variable "project" {
  type    = string
  default = "infraharvest-e2e-drift"
}

variable "team" {
  type    = string
  default = "web"
}

variable "retention" {
  type    = number
  default = 86400
}

variable "bucket_team" {
  type    = string
  default = "data"
}

locals {
  name = "infraharvest-e2e-drift"
  tags = { Project = var.project }
}

resource "aws_sqs_queue" "orders" {
  name                      = "${local.name}-orders"
  message_retention_seconds = var.retention
  tags                      = merge(local.tags, { team = var.team })
}

resource "aws_sqs_queue" "jobs" {
  name = "${local.name}-jobs"
  tags = local.tags
}

# A call of the curated s3-bucket module once imported, as in
# testdata/aws-options.
resource "aws_s3_bucket" "assets" {
  bucket = "${local.name}-assets"
  tags   = merge(local.tags, { team = var.bucket_team })
}

resource "aws_s3_bucket_versioning" "assets" {
  bucket = aws_s3_bucket.assets.id
  versioning_configuration {
    status = "Enabled"
  }
}

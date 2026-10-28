# Resources the environments sandbox test (TestSandboxAWSEnvironments, in
# sandbox_aws_environments_test.go) creates in a real AWS account before
# importing them: two small estates of one workload, dev and prd, in the
# one account and region, told apart by their Environment tag, as the
# configuration file's environments.<env>.tags map them. Each has a queue,
# of another visibility timeout in each, a bucket and a parameter named
# as a secret, whose value differs; prd alone has an alerts queue. Nothing
# bills by the hour: a run costs well under a cent, and the estate is
# destroyed after each run.
# The provider is configured through the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same account, such as one per engine."
  type        = string
  default     = "terraform"
}

variable "run_id" {
  description = "The CI run that applies the estate, if any. Every resource is tagged with it, so the run's last sweep finds what the run left, and nothing else."
  type        = string
  default     = ""
}

locals {
  # The tags every resource carries, so a sweep finds what a run left.
  sandbox_tags = { for k, v in { infraharvest-sandbox = var.suffix, infraharvest-sandbox-run = var.run_id } : k => v if v != "" }
}

provider "aws" {
  default_tags {
    tags = local.sandbox_tags
  }
}

data "aws_caller_identity" "current" {}

locals {
  name    = "ih-sbx-${var.suffix}"
  account = data.aws_caller_identity.current.account_id
  dev     = { Environment = "dev", Project = "infraharvest-sandbox" }
  prd     = { Environment = "prd", Project = "infraharvest-sandbox" }
}

# The same queue in each environment, of another size: the merged root
# sets its timeout from a variable each environment's var file sets.
resource "aws_sqs_queue" "dev_orders" {
  name                       = "${local.name}-dev-orders"
  visibility_timeout_seconds = 30
  message_retention_seconds  = 86400
  tags                       = local.dev
}

resource "aws_sqs_queue" "prd_orders" {
  name                       = "${local.name}-prd-orders"
  visibility_timeout_seconds = 120
  message_retention_seconds  = 86400
  tags                       = local.prd
}

# prd's alerts feature, which dev doesn't have: a deploy_ variable counts
# it in the merged root.
resource "aws_sqs_queue" "prd_alerts" {
  name                      = "${local.name}-prd-alerts"
  message_retention_seconds = 86400
  tags                      = local.prd
}

# A bucket in each, named after the account, as bucket names are global.
resource "aws_s3_bucket" "dev_data" {
  bucket        = "${local.name}-dev-${local.account}-data"
  force_destroy = true
  tags          = local.dev
}

resource "aws_s3_bucket" "prd_data" {
  bucket        = "${local.name}-prd-${local.account}-data"
  force_destroy = true
  tags          = local.prd
}

# A secret in each, of another value: the merged root takes it from a
# sensitive variable that no var file sets. String parameters, which
# infraharvest treats as secrets by their names (token), as it would a
# SecureString's value: the read-only role denies kms:Decrypt, so a
# SecureString fails to import by design (docs/permissions.md). Standard
# parameters cost nothing.
resource "aws_ssm_parameter" "dev_token" {
  name  = "/${local.name}-dev/token"
  type  = "String"
  value = "not-a-secret-${var.suffix}-dev"
  tags  = local.dev
}

resource "aws_ssm_parameter" "prd_token" {
  name  = "/${local.name}-prd/token"
  type  = "String"
  value = "not-a-secret-${var.suffix}-prd"
  tags  = local.prd
}

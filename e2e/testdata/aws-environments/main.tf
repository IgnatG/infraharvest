# Resources the end-to-end test of merging environments
# (environments_aws_test.go) creates in two accounts of the emulator of
# their own, dev and prod, which takes a 12-digit access key ID as the
# account. The endpoint and region come from the environment the test
# sets. The same queue is of another size in each; prod alone has an
# alerts feature, a topic and its queue; and a queue of each environment
# is configured too differently to merge.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  alias      = "dev"
  access_key = "777777777777"
  secret_key = "test"
}

provider "aws" {
  alias      = "prod"
  access_key = "888888888888"
  secret_key = "test"
}

variable "audit" {
  description = "Whether dev has an audit queue, which the test creates after the first import."
  type        = bool
  default     = false
}

locals {
  dev  = { Environment = "dev", Project = "shop" }
  prod = { Environment = "prd", Project = "shop" }
}

resource "aws_sqs_queue" "dev_orders" {
  provider                   = aws.dev
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
  tags                       = local.dev
}

resource "aws_sqs_queue" "prod_orders" {
  provider                   = aws.prod
  name                       = "shop-prd-orders"
  visibility_timeout_seconds = 120
  tags                       = local.prod
}

# Too differently configured to merge: each stays its environment's.
resource "aws_sqs_queue" "dev_events" {
  provider = aws.dev
  name     = "shop-dev-events"
  tags     = local.dev
}

resource "aws_sqs_queue" "prod_events" {
  provider                   = aws.prod
  name                       = "shop-prd-events"
  delay_seconds              = 5
  max_message_size           = 2048
  message_retention_seconds  = 86400
  receive_wait_time_seconds  = 10
  visibility_timeout_seconds = 45
  tags                       = local.prod
}

# The alerts feature, prod's only.
resource "aws_sns_topic" "prod_alerts" {
  provider = aws.prod
  name     = "shop-prd-alerts"
  tags     = local.prod
}

resource "aws_sqs_queue" "prod_alerts" {
  provider = aws.prod
  name     = "shop-prd-alerts"
  tags     = local.prod
}

# Created once the stack is merged, for --incremental.
resource "aws_sqs_queue" "dev_audit" {
  count    = var.audit ? 1 : 0
  provider = aws.dev
  name     = "shop-dev-audit"
  tags     = local.dev
}

# The role the test imports each account through. Floci checks that its
# trust policy lets the caller, the account 000000000000, assume it.
locals {
  trust_caller = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::000000000000:root" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role" "dev" {
  provider           = aws.dev
  name               = "infraharvest-readonly"
  assume_role_policy = local.trust_caller
}

resource "aws_iam_role" "prod" {
  provider           = aws.prod
  name               = "infraharvest-readonly"
  assume_role_policy = local.trust_caller
}

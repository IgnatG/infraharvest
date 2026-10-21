# Resources the end-to-end test of merging environments
# (environments_aws_test.go) creates in two accounts of the emulator of
# their own, dev and prod, which takes a 12-digit access key ID as the
# account. The endpoint and region come from the environment the test
# sets. The same queue is of another size in each; prod alone has an
# alerts feature, a topic and its queue; a queue of each environment is
# configured too differently to merge; three queues of one shape in each,
# with each environment's retention, become one resource with for_each;
# and each account has a bucket for the merged root's state. Some
# resources are created only later, for the incremental imports.

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

variable "dev_orders_timeout" {
  description = "The visibility timeout of dev's orders queue, which the test changes once the merged root is applied, for drift."
  type        = number
  default     = 30
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
  visibility_timeout_seconds = var.dev_orders_timeout
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

variable "alerts_delay" {
  description = "The delay of prod's alerts queue, which the test changes once the merged root is applied, for drift."
  type        = number
  default     = 0
}

resource "aws_sqs_queue" "prod_alerts" {
  provider      = aws.prod
  name          = "shop-prd-alerts"
  delay_seconds = var.alerts_delay
  tags          = local.prod
}

variable "dev_email_retention" {
  description = "The retention of dev's email queue, which the test changes once the merged root is applied, for drift."
  type        = number
  default     = 3600
}

# Three queues of one shape in each environment, whose retention differs
# between the environments: the merged root makes them one resource with
# for_each over a map variable each environment sets.
locals {
  channels = {
    email = { dev = var.dev_email_retention, prod = 86400 }
    sms   = { dev = 7200, prod = 172800 }
    push  = { dev = 10800, prod = 259200 }
  }
}

resource "aws_sqs_queue" "dev_channels" {
  for_each                   = local.channels
  provider                   = aws.dev
  name                       = "shop-dev-${each.key}"
  message_retention_seconds  = each.value.dev
  delay_seconds              = 15
  max_message_size           = 4096
  receive_wait_time_seconds  = 5
  visibility_timeout_seconds = 60
  tags                       = local.dev
}

resource "aws_sqs_queue" "prod_channels" {
  for_each                   = local.channels
  provider                   = aws.prod
  name                       = "shop-prd-${each.key}"
  message_retention_seconds  = each.value.prod
  delay_seconds              = 15
  max_message_size           = 4096
  receive_wait_time_seconds  = 5
  visibility_timeout_seconds = 60
  tags                       = local.prod
}

# The buckets of the merged root's S3 backend, one in each environment's
# account, which the import leaves out (--services=sqs,sns).
resource "aws_s3_bucket" "dev_state" {
  provider      = aws.dev
  bucket        = "shop-dev-tfstate"
  force_destroy = true
}

resource "aws_s3_bucket" "prod_state" {
  provider      = aws.prod
  bucket        = "shop-prd-tfstate"
  force_destroy = true
}

variable "reports" {
  description = "Whether dev has a reports queue, which the test creates for an incremental import of dev alone."
  type        = bool
  default     = false
}

variable "reports_tagged" {
  description = "Whether dev's reports queue has dev's tags; without, it leaves dev for the shared root, for --regenerate of the merged root."
  type        = bool
  default     = true
}

resource "aws_sqs_queue" "dev_reports" {
  count    = var.reports ? 1 : 0
  provider = aws.dev
  name     = "shop-dev-reports"
  tags     = var.reports_tagged ? local.dev : { Project = "shop" }
}

# Created once the stack is merged, for --incremental.
resource "aws_sqs_queue" "dev_audit" {
  count    = var.audit ? 1 : 0
  provider = aws.dev
  name     = "shop-dev-audit"
  tags     = local.dev
}

variable "dev_alerts" {
  description = "Whether dev has the alerts feature too, for --regenerate of the merged root: every environment then has it."
  type        = bool
  default     = false
}

resource "aws_sns_topic" "dev_alerts" {
  count    = var.dev_alerts ? 1 : 0
  provider = aws.dev
  name     = "shop-dev-alerts"
  tags     = local.dev
}

resource "aws_sqs_queue" "dev_alerts" {
  count    = var.dev_alerts ? 1 : 0
  provider = aws.dev
  name     = "shop-dev-alerts"
  tags     = local.dev
}

variable "scratch" {
  description = "Whether dev has a scratch queue, which the test adds to the merged root, then deletes, for what --incremental finds no longer in the cloud."
  type        = bool
  default     = false
}

resource "aws_sqs_queue" "dev_scratch" {
  count    = var.scratch ? 1 : 0
  provider = aws.dev
  name     = "shop-dev-scratch"
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

variable "billing" {
  description = "Whether prod has a billing queue, which the test creates for an incremental import from a fresh checkout of the output."
  type        = bool
  default     = false
}

resource "aws_sqs_queue" "prod_billing" {
  count    = var.billing ? 1 : 0
  provider = aws.prod
  name     = "shop-prd-billing"
  tags     = local.prod
}

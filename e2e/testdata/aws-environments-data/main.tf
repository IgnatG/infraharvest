# Resources the end-to-end test of a feature's data sources in merged
# environments (TestAWSEnvironmentsData, environments_aws_test.go)
# creates in two accounts of the emulator of their own, dev and prod.
# Prod alone has an alerts feature, a role with a policy attached that
# the test's catalogue overlay leaves out of the selection, so that the
# feature reads the policy through a data source no other resource
# uses.

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
  access_key = "717171717171"
  secret_key = "test"
}

provider "aws" {
  alias      = "prod"
  access_key = "818181818181"
  secret_key = "test"
}

# The role the test imports each account through, which both import.
# Floci checks that its trust policy lets the caller, the account
# 000000000000, assume it.
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

# The alerts feature, prod's only: its policy is left out, and read.
resource "aws_iam_policy" "prod_alerts" {
  provider = aws.prod
  name     = "shop-prd-alerts-publish"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sns:Publish"
      Resource = "*"
    }]
  })
}

resource "aws_iam_role" "prod_alerts" {
  provider = aws.prod
  name     = "shop-prd-alerts"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "prod_alerts" {
  provider   = aws.prod
  role       = aws_iam_role.prod_alerts.name
  policy_arn = aws_iam_policy.prod_alerts.arn
}

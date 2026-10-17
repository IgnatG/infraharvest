# Resources the parallel regions end-to-end test creates in an account of
# the emulator of their own, which takes a 12-digit access key ID as the
# account: a role, for the global root, and a log group in each of two
# regions. Floci keeps log groups by region, not by account, so the
# regions are ones testdata/aws leaves empty. The endpoint comes from the
# environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  alias      = "west"
  region     = "eu-west-1"
  access_key = "333333333333"
  secret_key = "test"
}

provider "aws" {
  alias      = "central"
  region     = "eu-central-1"
  access_key = "333333333333"
  secret_key = "test"
}

resource "aws_iam_role" "app" {
  provider = aws.west
  name     = "infraharvest-e2e-regions"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_cloudwatch_log_group" "west" {
  provider          = aws.west
  name              = "/infraharvest-e2e-regions/eu-west-1"
  retention_in_days = 7
}

resource "aws_cloudwatch_log_group" "central" {
  provider          = aws.central
  name              = "/infraharvest-e2e-regions/eu-central-1"
  retention_in_days = 7
}

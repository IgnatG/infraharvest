# Resources the parallel accounts end-to-end test creates in two accounts
# of the emulator, which takes a 12-digit access key ID as the account.
# Endpoint and region come from the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  alias      = "first"
  access_key = "111111111111"
  secret_key = "test"
}

provider "aws" {
  alias      = "second"
  access_key = "222222222222"
  secret_key = "test"
}

resource "aws_sqs_queue" "first" {
  provider = aws.first
  name     = "infraharvest-e2e-first-account"
}

resource "aws_sqs_queue" "second" {
  provider = aws.second
  name     = "infraharvest-e2e-second-account"
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

resource "aws_iam_role" "first" {
  provider           = aws.first
  name               = "infraharvest-readonly"
  assume_role_policy = local.trust_caller
}

resource "aws_iam_role" "second" {
  provider           = aws.second
  name               = "infraharvest-readonly"
  assume_role_policy = local.trust_caller
}

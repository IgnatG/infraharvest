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

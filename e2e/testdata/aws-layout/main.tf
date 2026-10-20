# Resources the end-to-end test of --regenerate with another --layout
# (moves_test.go) creates in an account of the emulator of its own, which
# takes a 12-digit access key ID as the account. The endpoint and region
# come from the environment the test sets. Imported into one root, then
# into a root per service, they are handed over from the first to the
# others.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = "121212121212"
  secret_key = "test"
}

resource "aws_sqs_queue" "orders" {
  name = "infraharvest-e2e-layout-orders"
}

resource "aws_sqs_queue" "jobs" {
  name = "infraharvest-e2e-layout-jobs"
}

resource "aws_sns_topic" "events" {
  name = "infraharvest-e2e-layout-events"
}

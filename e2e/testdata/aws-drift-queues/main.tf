# Resources the end-to-end tests of infraharvest drift on roots with an S3
# backend (drift_backend_test.go) and of the MCP server's drift tools
# (drift_mcp_test.go) create, each in an account of the emulator of its
# own, which takes a 12-digit access key ID as the account. The endpoint
# and region come from the environment the tests set. Applied again with
# another team, it changes a queue's tag out of band of the roots the
# import generated.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = var.account
  secret_key = "test"
}

variable "account" {
  type = string
}

variable "team" {
  type    = string
  default = "web"
}

locals {
  name = "infraharvest-e2e-drift-${var.account}"
}

# The bucket the roots keep their state in, which the import leaves out:
# it imports SQS only.
resource "aws_s3_bucket" "state" {
  bucket = "${local.name}-state"
}

# Two queues, so that the Project tag both have is the root's local.tags,
# and the team tag the first queue's own.
resource "aws_sqs_queue" "orders" {
  name = "${local.name}-orders"
  tags = { Project = "infraharvest-e2e-drift", team = var.team }
}

resource "aws_sqs_queue" "jobs" {
  name = "${local.name}-jobs"
  tags = { Project = "infraharvest-e2e-drift" }
}

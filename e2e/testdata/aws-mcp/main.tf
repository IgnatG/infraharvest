# Resources the end-to-end test of the MCP server's tools (mcp_test.go)
# creates in two accounts of the emulator of its own, one per environment,
# which takes a 12-digit access key ID as the account. The endpoint and
# region come from the environment the test sets. The environment's name
# is in the queues' names and tags, as in hand-written multi-environment
# code. Applied again with another team, it changes a queue's tag out of
# band of the roots the import generated.

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
  description = "The account to create the queues in."
  type        = string
}

variable "env" {
  description = "The environment the account holds."
  type        = string
}

variable "team" {
  description = "The orders queue's team tag, which the test changes once the roots are applied, for drift."
  type        = string
  default     = "web"
}

resource "aws_sqs_queue" "orders" {
  name = "mcp-${var.env}-orders"
  tags = { Environment = var.env, team = var.team }
}

resource "aws_sqs_queue" "jobs" {
  name = "mcp-${var.env}-jobs"
  tags = { Environment = var.env }
}

# Resources the end-to-end test of --regenerate's moved and removed
# blocks (moves_test.go) creates in an account of the emulator of its
# own, which takes a 12-digit access key ID as the account. The endpoint
# and region come from the environment the test sets. Applied again with
# other variables, the first queue gets another Name tag, which gives it
# another address, and the second another environment, which puts it in
# another root.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = "555555555555"
  secret_key = "test"
}

variable "renamed_name" {
  type    = string
  default = "infraharvest-e2e-moves-before"
}

variable "handed_env" {
  type    = string
  default = "dev"
}

resource "aws_sqs_queue" "renamed" {
  name = "infraharvest-e2e-moves-renamed"
  tags = { Name = var.renamed_name, Env = "dev" }
}

resource "aws_sqs_queue" "handed" {
  name = "infraharvest-e2e-moves-handed"
  tags = { Env = var.handed_env }
}

# A queue that stays as it is, so that the root keeps more than one queue
# and labels them by name, and keeps its address.
resource "aws_sqs_queue" "kept" {
  name = "infraharvest-e2e-moves-kept"
  tags = { Env = "dev" }
}

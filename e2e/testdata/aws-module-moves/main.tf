# Resources the end-to-end test of --regenerate's moves into and out of
# generated local modules (moves_test.go) creates in an account of the
# emulator of its own, which takes a 12-digit access key ID as the
# account. The endpoint and region come from the environment the test
# sets. Two roles, each with a policy of its own, are two clusters of one
# shape, which --modules=local makes calls of one generated module, and
# --modules=none keeps in the root.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = "999999999999"
  secret_key = "test"
}

resource "aws_iam_role" "app" {
  name = "infraharvest-e2e-module-moves-app"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "app" {
  name = "queue"
  role = aws_iam_role.app.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:SendMessage"
      Resource = "*"
    }]
  })
}

resource "aws_iam_role" "worker" {
  name = "infraharvest-e2e-module-moves-worker"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "states.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "worker" {
  name = "logs"
  role = aws_iam_role.worker.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "logs:CreateLogDelivery"
      Resource = "*"
    }]
  })
}

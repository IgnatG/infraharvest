# Resources the end-to-end tests of the command-line options
# (options_test.go) create in an account of the emulator of their own,
# which takes a 12-digit access key ID as the account. The endpoint and
# region come from the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  access_key = "444444444444"
  secret_key = "test"
}

locals {
  name = "infraharvest-e2e-options"
  tags = { Project = local.name }
}

# A network whose subnet and security group, in a root per service, refer
# to the VPC another root manages.
resource "aws_vpc" "main" {
  cidr_block = "10.60.0.0/16"
  tags       = { Name = local.name }
}

resource "aws_subnet" "app" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.60.1.0/24"
  availability_zone = "us-east-1a"
  tags              = { Name = "${local.name}-app" }
}

resource "aws_security_group" "web" {
  name        = "${local.name}-web"
  description = "HTTPS from the private network"
  vpc_id      = aws_vpc.main.id

  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["10.0.0.0/8"]
  }
}

# Two roles of the same shape, each with an inline policy the iam-role
# module can't express: they share a generated local module.
resource "aws_iam_role" "app" {
  name = "${local.name}-app"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy" "app" {
  name = "queue"
  role = aws_iam_role.app.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:SendMessage"
      Resource = aws_sqs_queue.orders.arn
    }]
  })
}

resource "aws_iam_role" "worker" {
  name = "${local.name}-worker"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "states.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.tags
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

# Two buckets, each a call of the curated s3-bucket module by default.
resource "aws_s3_bucket" "assets" {
  bucket = "${local.name}-assets"
}

resource "aws_s3_bucket_versioning" "assets" {
  bucket = aws_s3_bucket.assets.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket" "backups" {
  bucket = "${local.name}-backups"
}

resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

# Queues: one of the web team's, whose retention verify checks, one
# without tags, and one whose tag says another Terraform state manages it,
# which an import leaves out unless --include-owned.
resource "aws_sqs_queue" "orders" {
  name                      = "${local.name}-orders"
  message_retention_seconds = 86400
  tags                      = { team = "web" }
}

resource "aws_sqs_queue" "jobs" {
  name = "${local.name}-jobs"
}

resource "aws_sqs_queue" "owned" {
  name = "${local.name}-owned"
  tags = { team = "platform", ManagedBy = "Terraform" }
}

# Topics: the legacy one is left out by the skip rule of
# testdata/catalogue/skip.
resource "aws_sns_topic" "events" {
  name = "${local.name}-events"
  tags = { team = "data" }
}

resource "aws_sns_topic" "legacy" {
  name = "${local.name}-legacy"
}

# Volumes for the tag filters, which the EBS lister passes to
# DescribeVolumes.
resource "aws_ebs_volume" "web" {
  availability_zone = "us-east-1a"
  size              = 1
  type              = "gp3"
  tags              = { team = "web" }
}

resource "aws_ebs_volume" "data" {
  availability_zone = "us-east-1a"
  size              = 1
  type              = "gp3"
  tags              = { team = "data" }
}

resource "aws_ebs_volume" "scratch" {
  availability_zone = "us-east-1a"
  size              = 1
  type              = "gp3"
}

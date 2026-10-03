# Resources the end-to-end test creates in the emulator before importing
# them. The provider is configured only through the environment the test
# sets (endpoint, region, test credentials), like infraharvest's output.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {}

locals {
  name = "infraharvest-e2e"
  tags = { Project = "infraharvest-e2e" }
}

resource "aws_vpc" "main" {
  cidr_block = "10.42.0.0/16"
  tags       = local.tags
}

resource "aws_subnet" "a" {
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.42.1.0/24"
  tags       = local.tags
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "a" {
  subnet_id      = aws_subnet.a.id
  route_table_id = aws_route_table.public.id
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

resource "aws_iam_policy" "app" {
  name = "${local.name}-app"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:SendMessage"
      Resource = aws_sqs_queue.jobs.arn
    }]
  })
}

resource "aws_sqs_queue" "jobs" {
  name = "${local.name}-jobs"
  tags = local.tags
}

resource "aws_sns_topic" "events" {
  name = "${local.name}-events"
}

resource "aws_dynamodb_table" "items" {
  name         = "${local.name}-items"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }
}

resource "aws_cloudwatch_log_group" "app" {
  name              = "/${local.name}/app"
  retention_in_days = 7
}

resource "aws_kms_key" "app" {
  description             = "${local.name} application key"
  deletion_window_in_days = 7
}

resource "aws_ecr_repository" "app" {
  name = "${local.name}-app"
}

resource "aws_kinesis_stream" "events" {
  name        = "${local.name}-events"
  shard_count = 1
}

resource "aws_secretsmanager_secret" "db" {
  name = "${local.name}-db"
}

resource "aws_route53_zone" "internal" {
  name = "${local.name}.internal"
}

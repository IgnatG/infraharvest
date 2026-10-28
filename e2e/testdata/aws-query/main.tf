# The resources the query service's end-to-end test creates in an account
# of the emulator of its own, which takes a 12-digit access key ID as the
# account: a VPC flow log, a type no lister lists, which the query service
# lists through Terraform's list resources, with the VPC, log group and
# role it needs. Floci keeps log groups by region, not by account, so the
# region is one no other test uses. The endpoint comes from the
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
  region     = "ap-southeast-2"
  access_key = "131313131313"
  secret_key = "test"
}

resource "aws_vpc" "main" {
  cidr_block = "10.42.0.0/16"
}

resource "aws_cloudwatch_log_group" "flows" {
  name              = "/infraharvest-e2e-query/flows"
  retention_in_days = 7
}

resource "aws_iam_role" "flows" {
  name = "infraharvest-e2e-query-flows"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "vpc-flow-logs.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_flow_log" "vpc" {
  vpc_id               = aws_vpc.main.id
  traffic_type         = "REJECT"
  log_destination_type = "cloud-watch-logs"
  log_destination      = aws_cloudwatch_log_group.flows.arn
  iam_role_arn         = aws_iam_role.flows.arn
}

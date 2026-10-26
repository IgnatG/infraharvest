# ---- data.tf ----

data "aws_region" "current" {}

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

# ---- locals.tf ----

locals {
  environment_size = {
    dev = {
      NatCount     = 0
      InstanceType = "t3.micro"
    }
    prod = {
      NatCount     = 1
      InstanceType = "m6i.large"
    }
  }
}

locals {
  is_prod    = var.environment == "prod"
  is_not_dev = !(var.environment == "dev")
  wants_nat  = local.is_prod && local.is_not_dev
}

# ---- main.tf ----

# Vpc (AWS::EC2::VPC)
resource "aws_vpc" "vpc" {
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags = {
    Name        = "${var.stack_name}-vpc"
    Environment = var.environment
  }
}

# InternetGateway (AWS::EC2::InternetGateway)
resource "aws_internet_gateway" "internet_gateway" {}

# GatewayAttachment (AWS::EC2::VPCGatewayAttachment)
resource "aws_internet_gateway_attachment" "gateway_attachment" {
  vpc_id              = aws_vpc.vpc.id
  internet_gateway_id = aws_internet_gateway.internet_gateway.id
}

# PublicSubnetA (AWS::EC2::Subnet)
resource "aws_subnet" "public_subnet_a" {
  vpc_id                  = aws_vpc.vpc.id
  availability_zone       = element(data.aws_availability_zones.available.names, 0)
  cidr_block              = element([for b in [aws_vpc.vpc.cidr_block] : [for i in range(4) : cidrsubnet(b, (strcontains(b, ":") ? 128 : 32) - 8 - tonumber(split("/", b)[1]), i)]][0], 0)
  map_public_ip_on_launch = true
}

# PublicSubnetB (AWS::EC2::Subnet)
resource "aws_subnet" "public_subnet_b" {
  vpc_id                  = aws_vpc.vpc.id
  availability_zone       = element(data.aws_availability_zones.available.names, 1)
  cidr_block              = element(cidrsubnets("10.20.0.0/16", 8, 8, 8, 8), 1)
  map_public_ip_on_launch = true
}

# PublicRoutes (AWS::EC2::RouteTable)
resource "aws_route_table" "public_routes" {
  vpc_id = aws_vpc.vpc.id
}

# DefaultRoute (AWS::EC2::Route)
resource "aws_route" "default_route" {
  route_table_id         = aws_route_table.public_routes.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.internet_gateway.id
  depends_on             = [aws_internet_gateway_attachment.gateway_attachment]
}

# NatAddress (AWS::EC2::EIP)
resource "aws_eip" "nat_address" {
  count  = local.wants_nat ? 1 : 0
  domain = "vpc"
}

# NatGateway (AWS::EC2::NatGateway)
resource "aws_nat_gateway" "nat_gateway" {
  count         = local.wants_nat ? 1 : 0
  allocation_id = aws_eip.nat_address[0].allocation_id
  subnet_id     = aws_subnet.public_subnet_a.id
}

# BastionGroup (AWS::EC2::SecurityGroup)
resource "aws_security_group" "bastion_group" {
  description = "SSH to the ${var.environment} bastion"
  vpc_id      = aws_vpc.vpc.id
  ingress {
    protocol    = "tcp"
    from_port   = 22
    to_port     = 22
    cidr_blocks = [element(var.admin_cidrs, 0)]
  }
  dynamic "ingress" {
    for_each = local.is_prod ? [1] : []
    content {
      protocol    = "tcp"
      from_port   = 22
      to_port     = 22
      cidr_blocks = [element(var.admin_cidrs, 1)]
    }
  }
}

# ---- outputs.tf ----

# CloudFormation exports this value as {"Fn::Sub":"${AWS::StackName}-VpcId"}. Terraform has no exports: another configuration reads the output from this one's state, with a terraform_remote_state data source
output "vpc_id" {
  description = "The VPC."
  value       = aws_vpc.vpc.id
}

output "public_subnets" {
  value = join(",", [aws_subnet.public_subnet_a.id, aws_subnet.public_subnet_b.id])
}

output "nat_address" {
  value = local.wants_nat ? aws_eip.nat_address[0].public_ip : null
}

output "instance_type" {
  value = local.environment_size[var.environment]["InstanceType"]
}

output "region" {
  value = "${data.aws_region.current.region} in ${data.aws_partition.current.partition} for ${data.aws_caller_identity.current.account_id}"
}

# ---- variables.tf ----

variable "environment" {
  type        = string
  description = "Which environment the network is for."
  default     = "dev"
  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "The value of Environment must be one of dev, staging, prod."
  }
}

variable "vpc_cidr" {
  type    = string
  default = "10.20.0.0/16"
  validation {
    condition     = can(regex("^(?:(\\d{1,3}\\.){3}\\d{1,3}/\\d{1,2})$", var.vpc_cidr))
    error_message = "Must be an IPv4 CIDR block."
  }
}

variable "key_name" {
  type        = string
  description = "Key pair for the bastion. (CloudFormation type AWS::EC2::KeyPair::KeyName)"
}

variable "admin_cidrs" {
  type    = list(string)
  default = ["203.0.113.0/24", "198.51.100.0/24"]
}

variable "stack_name" {
  type        = string
  description = "Stands in for AWS::StackName: Terraform has no stacks, so set it to the name the template's resources were named after."
}

# ---- diagnostics ----
# info: Resources.Vpc.Properties.Tags[0].Value.Fn::Sub: AWS::StackName is the variable stack_name: Terraform has no stacks
# info: Outputs.VpcId.Export: CloudFormation exports this value as {"Fn::Sub":"${AWS::StackName}-VpcId"}. Terraform has no exports: another configuration reads the output from this one's state, with a terraform_remote_state data source

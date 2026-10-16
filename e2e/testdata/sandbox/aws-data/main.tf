# Resources the data sandbox test (TestSandboxAWSData, in
# sandbox_aws_test.go) creates in a real AWS account before importing
# them: an EC2 instance, a Lambda function, a PostgreSQL database, a
# Redshift cluster and an S3 bucket, in a VPC of their own. The database
# and the cluster cost by the hour, so they are an estate of their own,
# which a run creates only when asked to: the smallest of each, in one
# zone, without backups or final snapshots, with no NAT gateway, and
# destroyed as soon as the run has imported and planned them.
# The provider is configured through the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.7"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same account, such as one per engine."
  type        = string
  default     = "terraform"
}

variable "redshift_node_type" {
  description = "The node type of the single-node Redshift cluster: the cheapest current one (DC2 is retired for new clusters)."
  type        = string
  default     = "ra3.large"
}

variable "run_id" {
  description = "The CI run that applies the estate, if any. Every resource is tagged with it, so the run's last sweep finds what the run left, and nothing else."
  type        = string
  default     = ""
}

locals {
  # The tags every resource carries, so a sweep finds what a run left.
  sandbox_tags = { for k, v in { infraharvest-sandbox = var.suffix, infraharvest-sandbox-run = var.run_id } : k => v if v != "" }
}

provider "aws" {
  default_tags {
    tags = local.sandbox_tags
  }
}

data "aws_caller_identity" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name = "ih-sbx-${var.suffix}-data"
  tags = { Project = "infraharvest-sandbox" }
  # RDS and Redshift subnet groups need subnets in two zones.
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

# Private subnets only: nothing here needs to reach the internet.
resource "aws_vpc" "main" {
  cidr_block           = "10.63.0.0/16"
  enable_dns_hostnames = true
  tags                 = merge(local.tags, { Name = local.name })
}

resource "aws_subnet" "private" {
  for_each          = { for i, az in local.azs : az => "10.63.${i + 11}.0/24" }
  vpc_id            = aws_vpc.main.id
  availability_zone = each.key
  cidr_block        = each.value
  tags              = merge(local.tags, { Name = "${local.name}-private-${each.key}" })
}

# One group for the database and the cluster, open to the VPC only.
resource "aws_security_group" "data" {
  name        = "${local.name}-data"
  description = "PostgreSQL and Redshift from the VPC"
  vpc_id      = aws_vpc.main.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "postgres" {
  security_group_id = aws_security_group.data.id
  cidr_ipv4         = aws_vpc.main.cidr_block
  from_port         = 5432
  to_port           = 5432
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_ingress_rule" "redshift" {
  security_group_id = aws_security_group.data.id
  cidr_ipv4         = aws_vpc.main.cidr_block
  from_port         = 5439
  to_port           = 5439
  ip_protocol       = "tcp"
}

# The smallest Graviton instance, with no public address.
data "aws_ssm_parameter" "al2023" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_instance" "app" {
  ami                    = nonsensitive(data.aws_ssm_parameter.al2023.value)
  instance_type          = "t4g.nano"
  subnet_id              = aws_subnet.private[local.azs[0]].id
  vpc_security_group_ids = [aws_security_group.data.id]
  tags                   = merge(local.tags, { Name = "${local.name}-app" })

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    volume_size = 8
    volume_type = "gp3"
    encrypted   = true
  }
}

# A Lambda function from a zip of inline code, logging to a group of its
# own (the default selection leaves out the groups Lambda creates).
data "archive_file" "fn" {
  type        = "zip"
  output_path = "${path.module}/fn.zip"
  source {
    content  = "def handler(event, context):\n    return {\"ok\": True}\n"
    filename = "index.py"
  }
}

data "aws_iam_policy_document" "lambda_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fn" {
  name               = "${local.name}-fn"
  assume_role_policy = data.aws_iam_policy_document.lambda_trust.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "fn_logs" {
  role       = aws_iam_role.fn.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_cloudwatch_log_group" "fn" {
  name              = "/${local.name}/fn"
  retention_in_days = 1
  tags              = local.tags
}

resource "aws_lambda_function" "fn" {
  function_name    = "${local.name}-fn"
  role             = aws_iam_role.fn.arn
  runtime          = "python3.13"
  handler          = "index.handler"
  architectures    = ["arm64"]
  filename         = data.archive_file.fn.output_path
  source_code_hash = data.archive_file.fn.output_base64sha256
  tags             = local.tags
  depends_on       = [aws_cloudwatch_log_group.fn, aws_iam_role_policy_attachment.fn_logs]

  logging_config {
    log_format = "Text"
    log_group  = aws_cloudwatch_log_group.fn.name
  }
}

# A bucket, emptied when it is destroyed.
resource "aws_s3_bucket" "data" {
  bucket        = "${local.name}-${data.aws_caller_identity.current.account_id}"
  force_destroy = true
  tags          = local.tags
}

resource "aws_s3_bucket_public_access_block" "data" {
  bucket                  = aws_s3_bucket.data.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# The smallest PostgreSQL instance, in one zone. RDS keeps the master
# password in Secrets Manager, so it is nowhere in the configuration or
# the state. Without backups, RDS takes none after creating it, and
# deletes it without a final snapshot.
resource "aws_db_subnet_group" "main" {
  name       = local.name
  subnet_ids = [for s in aws_subnet.private : s.id]
  tags       = local.tags
}

resource "aws_db_instance" "main" {
  identifier                  = "${local.name}-db"
  engine                      = "postgres"
  engine_version              = "17"
  instance_class              = "db.t4g.micro"
  allocated_storage           = 20
  storage_type                = "gp3"
  storage_encrypted           = true
  username                    = "ihadmin"
  manage_master_user_password = true
  db_subnet_group_name        = aws_db_subnet_group.main.name
  vpc_security_group_ids      = [aws_security_group.data.id]
  multi_az                    = false
  backup_retention_period     = 0
  skip_final_snapshot         = true
  deletion_protection         = false
  apply_immediately           = true
  tags                        = local.tags
}

# A single-node Redshift cluster, private, encrypted as Redshift encrypts
# every new cluster, with the admin password in Secrets Manager, and no
# final snapshot. RA3 clusters keep automated snapshots for at least a day;
# Redshift deletes them with the cluster.
resource "aws_redshift_subnet_group" "main" {
  name       = local.name
  subnet_ids = [for s in aws_subnet.private : s.id]
  tags       = local.tags
}

resource "aws_redshift_cluster" "main" {
  cluster_identifier                  = "${local.name}-dw"
  node_type                           = var.redshift_node_type
  cluster_type                        = "single-node"
  database_name                       = "analytics"
  master_username                     = "ihadmin"
  manage_master_password              = true
  cluster_subnet_group_name           = aws_redshift_subnet_group.main.name
  vpc_security_group_ids              = [aws_security_group.data.id]
  publicly_accessible                 = false
  automated_snapshot_retention_period = 1
  skip_final_snapshot                 = true
  tags                                = local.tags
}

# Resources the clusters sandbox test (TestSandboxAWSClusters, in
# sandbox_aws_test.go) creates in a real AWS account before importing
# them: a PostgreSQL database, a Valkey cache and an EKS control plane,
# in a VPC of their own. They take minutes to create and delete and cost
# by the hour, so they are an estate of their own, which a run creates
# only when asked to: the smallest of each, in one zone where they can,
# with no NAT gateway and no Kubernetes nodes. It is destroyed after each
# run.
# The provider is configured through the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same account, such as one per engine."
  type        = string
  default     = "terraform"
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
  name = "ih-sbx-${var.suffix}-clusters"
  tags = { Project = "infraharvest-sandbox" }
  # RDS and ElastiCache subnet groups, and EKS, need subnets in two zones.
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

# Private subnets only: nothing here needs to reach the internet, and the
# EKS endpoint is public.
resource "aws_vpc" "main" {
  cidr_block           = "10.62.0.0/16"
  enable_dns_hostnames = true
  tags                 = merge(local.tags, { Name = local.name })
}

resource "aws_subnet" "private" {
  for_each          = { for i, az in local.azs : az => "10.62.${i + 11}.0/24" }
  vpc_id            = aws_vpc.main.id
  availability_zone = each.key
  cidr_block        = each.value
  tags              = merge(local.tags, { Name = "${local.name}-private-${each.key}" })
}

# A group each for the database and the cache, open to the VPC only.
resource "aws_security_group" "db" {
  name        = "${local.name}-db"
  description = "PostgreSQL from the VPC"
  vpc_id      = aws_vpc.main.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "db" {
  security_group_id = aws_security_group.db.id
  cidr_ipv4         = aws_vpc.main.cidr_block
  from_port         = 5432
  to_port           = 5432
  ip_protocol       = "tcp"
}

resource "aws_security_group" "cache" {
  name        = "${local.name}-cache"
  description = "Valkey from the VPC"
  vpc_id      = aws_vpc.main.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "cache" {
  security_group_id = aws_security_group.cache.id
  cidr_ipv4         = aws_vpc.main.cidr_block
  from_port         = 6379
  to_port           = 6379
  ip_protocol       = "tcp"
}

# The smallest PostgreSQL instance, in one zone, with a parameter group of
# its own. RDS keeps the master password in Secrets Manager, so it is
# nowhere in the configuration or the state. Without backups, RDS takes
# none after creating it, and deletes it without a final snapshot.
resource "aws_db_subnet_group" "main" {
  name       = local.name
  subnet_ids = [for s in aws_subnet.private : s.id]
  tags       = local.tags
}

resource "aws_db_parameter_group" "postgres" {
  name   = "${local.name}-postgres17"
  family = "postgres17"
  tags   = local.tags

  parameter {
    name  = "log_min_duration_statement"
    value = "1000"
  }
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
  parameter_group_name        = aws_db_parameter_group.postgres.name
  vpc_security_group_ids      = [aws_security_group.db.id]
  multi_az                    = false
  backup_retention_period     = 0
  skip_final_snapshot         = true
  deletion_protection         = false
  apply_immediately           = true
  tags                        = local.tags
}

# The smallest Valkey node, alone in its replication group, with a
# parameter group of its own.
resource "aws_elasticache_subnet_group" "main" {
  name       = local.name
  subnet_ids = [for s in aws_subnet.private : s.id]
  tags       = local.tags
}

resource "aws_elasticache_parameter_group" "valkey" {
  name   = "${local.name}-valkey8"
  family = "valkey8"
  tags   = local.tags

  parameter {
    name  = "maxmemory-policy"
    value = "allkeys-lru"
  }
}

resource "aws_elasticache_replication_group" "main" {
  replication_group_id       = local.name
  description                = "infraharvest sandbox cache"
  engine                     = "valkey"
  engine_version             = "8.0"
  node_type                  = "cache.t4g.micro"
  num_cache_clusters         = 1
  automatic_failover_enabled = false
  multi_az_enabled           = false
  parameter_group_name       = aws_elasticache_parameter_group.valkey.name
  subnet_group_name          = aws_elasticache_subnet_group.main.name
  security_group_ids         = [aws_security_group.cache.id]
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  snapshot_retention_limit   = 0
  apply_immediately          = true
  tags                       = local.tags
}

# An EKS control plane with no nodes, and no add-ons, which would wait for
# nodes. Access is through access entries alone (API), and EKS makes none
# for the fixture role that creates the cluster: the import would pick that
# one up as the estate's, and a cluster created again from the generated
# configuration would make it again, in the way of the imported one. The
# estate grants access the way the eks module does, through an entry and a
# policy of its own, here a read-only one for a role of its own.
data "aws_iam_policy_document" "eks_trust" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks" {
  name               = "${local.name}-eks"
  assume_role_policy = data.aws_iam_policy_document.eks_trust.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "eks" {
  role       = aws_iam_role.eks.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_eks_cluster" "main" {
  name                          = local.name
  role_arn                      = aws_iam_role.eks.arn
  bootstrap_self_managed_addons = false
  tags                          = local.tags

  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = false
  }

  vpc_config {
    subnet_ids = [for s in aws_subnet.private : s.id]
  }

  # Standard support only: extended support costs extra.
  upgrade_policy {
    support_type = "STANDARD"
  }

  depends_on = [aws_iam_role_policy_attachment.eks]
}

data "aws_iam_policy_document" "viewer_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }
}

# Allowed nothing in AWS: only to read the cluster's default namespace.
resource "aws_iam_role" "viewer" {
  name               = "${local.name}-viewer"
  assume_role_policy = data.aws_iam_policy_document.viewer_trust.json
  tags               = local.tags
}

resource "aws_eks_access_entry" "viewer" {
  cluster_name  = aws_eks_cluster.main.name
  principal_arn = aws_iam_role.viewer.arn
  tags          = local.tags
}

resource "aws_eks_access_policy_association" "viewer" {
  cluster_name  = aws_eks_cluster.main.name
  principal_arn = aws_eks_access_entry.viewer.principal_arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSViewPolicy"

  access_scope {
    type       = "namespace"
    namespaces = ["default"]
  }
}

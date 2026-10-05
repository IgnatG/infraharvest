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
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.42.1.0/24"
  availability_zone = "us-east-1a"
  tags              = local.tags
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

# Terraform doesn't write parameter values into the configuration it
# generates; infraharvest turns them into variables the test then sets.
resource "aws_ssm_parameter" "endpoint" {
  name  = "/${local.name}/endpoint"
  type  = "String"
  value = "https://api.${local.name}.internal"
}

resource "aws_ssm_parameter" "token" {
  name  = "/${local.name}/token"
  type  = "SecureString"
  value = "not-a-real-secret"
}

resource "aws_route53_zone" "internal" {
  name = "${local.name}.internal"
}

resource "aws_subnet" "b" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.42.2.0/24"
  availability_zone = "us-east-1b"
  tags              = local.tags
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.a.id
  tags          = local.tags
}

resource "aws_network_acl" "private" {
  vpc_id     = aws_vpc.main.id
  subnet_ids = [aws_subnet.b.id]

  ingress {
    rule_no    = 100
    action     = "allow"
    protocol   = "tcp"
    from_port  = 443
    to_port    = 443
    cidr_block = "10.0.0.0/8"
  }

  egress {
    rule_no    = 100
    action     = "allow"
    protocol   = "-1"
    from_port  = 0
    to_port    = 0
    cidr_block = "0.0.0.0/0"
  }
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.us-east-1.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.public.id]
}

# Holds the generated roots' state (see stateBackendConfig in aws_test.go).
# Like every bucket here, it becomes a terraform-aws-modules/s3-bucket call.
resource "aws_s3_bucket" "state" {
  bucket = "infraharvest-e2e-state"
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket" "logs" {
  bucket = "infraharvest-e2e-logs"
}

resource "aws_s3_bucket_versioning" "logs" {
  bucket = aws_s3_bucket.logs.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket" "artifacts" {
  bucket = "${local.name}-artifacts"
  tags   = local.tags
}

resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    id     = "expire-old-builds"
    status = "Enabled"
    filter {
      prefix = "builds/"
    }
    expiration {
      days = 90
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = ["${aws_s3_bucket.artifacts.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}

# The VPC's default security group is one the default selection leaves out:
# the import refers to it through a data source.
resource "aws_lb" "web" {
  name               = "${local.name}-web"
  load_balancer_type = "application"
  internal           = true
  subnets            = [aws_subnet.a.id, aws_subnet.b.id]
  security_groups    = [aws_security_group.web.id, aws_vpc.main.default_security_group_id]
}

resource "aws_lb_target_group" "web" {
  name     = "${local.name}-web"
  port     = 8080
  protocol = "HTTP"
  vpc_id   = aws_vpc.main.id
}

resource "aws_lb_listener" "web" {
  load_balancer_arn = aws_lb.web.arn
  port              = 8080
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.web.arn
  }
}

resource "aws_ecs_cluster" "apps" {
  name = "${local.name}-apps"
}

resource "aws_ecs_task_definition" "web" {
  family = "${local.name}-web"
  container_definitions = jsonencode([{
    name      = "web"
    image     = "public.ecr.aws/nginx/nginx:stable"
    essential = true
    memory    = 128
  }])
}

# No tasks run: the emulator would start containers for them.
resource "aws_ecs_service" "web" {
  name            = "web"
  cluster         = aws_ecs_cluster.apps.id
  task_definition = aws_ecs_task_definition.web.arn
  desired_count   = 0
}

resource "aws_cloudwatch_metric_alarm" "queue_depth" {
  alarm_name          = "${local.name}-queue-depth"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.jobs.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 100
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.events.arn]
}

resource "aws_cloudwatch_event_rule" "nightly" {
  name                = "${local.name}-nightly"
  schedule_expression = "cron(0 3 * * ? *)"
}

resource "aws_cloudwatch_event_target" "nightly" {
  rule      = aws_cloudwatch_event_rule.nightly.name
  target_id = "jobs"
  arn       = aws_sqs_queue.jobs.arn
}

resource "aws_iam_role" "workflow" {
  name = "${local.name}-workflow"
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

# The two roles and their inline policies have the same shape, so they share
# a generated local module.
resource "aws_iam_role_policy" "app" {
  name = "queue"
  role = aws_iam_role.app.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:SendMessage"
      Resource = aws_sqs_queue.jobs.arn
    }]
  })
}

resource "aws_iam_role_policy" "workflow" {
  name = "logs"
  role = aws_iam_role.workflow.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "logs:CreateLogDelivery"
      Resource = "*"
    }]
  })
}

# A role shaped like the iam-role module's: an inline policy and an
# instance profile named after it, and an attached policy.
resource "aws_iam_role" "ci" {
  name = "${local.name}-ci"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "ci" {
  name = aws_iam_role.ci.name
  role = aws_iam_role.ci.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "s3:GetObject"
      Resource = "${aws_s3_bucket.artifacts.arn}/*"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ci" {
  role       = aws_iam_role.ci.name
  policy_arn = aws_iam_policy.app.arn
}

resource "aws_iam_instance_profile" "ci" {
  name = aws_iam_role.ci.name
  role = aws_iam_role.ci.name
}

resource "aws_sfn_state_machine" "workflow" {
  name     = "${local.name}-workflow"
  role_arn = aws_iam_role.workflow.arn
  definition = jsonencode({
    StartAt = "Done"
    States  = { Done = { Type = "Succeed" } }
  })
}

resource "aws_ebs_volume" "data" {
  availability_zone = "us-east-1a"
  size              = 1
  type              = "gp3"
  tags              = local.tags
}

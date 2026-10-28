# Resources the sandbox test (sandbox_aws_test.go) creates in a real AWS
# account before importing them: the types the Floci estate can't cover,
# or covers only partly, such as VPCs with NAT gateways, an EC2 instance,
# a Lambda function, REST and HTTP APIs, WAF and CloudFront, and a queue
# and a parameter in a second region. It is small, cheap and destroyed
# after each run.
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

variable "second_region" {
  description = "A region besides the provider's, which an import of both keeps in a root of its own."
  type        = string
  default     = "eu-west-1"
}

variable "extra" {
  description = "Adds a log group, which an incremental import adds to the roots of the first."
  type        = bool
  default     = false
}

variable "extra_name" {
  description = "A Name tag for the extra log group, which gives it another address when the roots are generated again."
  type        = string
  default     = ""
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

provider "aws" {
  alias  = "second"
  region = var.second_region

  default_tags {
    tags = local.sandbox_tags
  }
}

data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name  = "ih-sbx-${var.suffix}"
  zonal = "${local.name}-zonal"
  tags  = { Project = "infraharvest-sandbox" }
  azs   = slice(data.aws_availability_zones.available.names, 0, 2)
}

# A VPC built the way terraform-aws-modules/vpc builds one, named after
# the VPC: public subnets routed to the internet gateway, private subnets
# to one NAT gateway in the first public subnet. It becomes a call of the
# module.
resource "aws_vpc" "main" {
  cidr_block           = "10.60.0.0/16"
  enable_dns_hostnames = true
  tags                 = merge(local.tags, { Name = local.name })
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
  tags   = merge(local.tags, { Name = local.name })
}

resource "aws_subnet" "public" {
  for_each          = { for i, az in local.azs : az => "10.60.${i + 1}.0/24" }
  vpc_id            = aws_vpc.main.id
  availability_zone = each.key
  cidr_block        = each.value
  tags              = merge(local.tags, { Name = "${local.name}-public-${each.key}" })
}

resource "aws_subnet" "private" {
  for_each          = { for i, az in local.azs : az => "10.60.${i + 11}.0/24" }
  vpc_id            = aws_vpc.main.id
  availability_zone = each.key
  cidr_block        = each.value
  tags              = merge(local.tags, { Name = "${local.name}-private-${each.key}" })
}

resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = merge(local.tags, { Name = "${local.name}-${local.azs[0]}" })
}

resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[local.azs[0]].id
  tags          = merge(local.tags, { Name = "${local.name}-${local.azs[0]}" })
  depends_on    = [aws_internet_gateway.main]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
  tags   = merge(local.tags, { Name = "${local.name}-public" })

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id
  tags   = merge(local.tags, { Name = "${local.name}-private" })

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  for_each       = aws_subnet.public
  subnet_id      = each.value.id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "private" {
  for_each       = aws_subnet.private
  subnet_id      = each.value.id
  route_table_id = aws_route_table.private.id
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.private.id]
  tags              = local.tags
}

# A second VPC, built the way the module builds one with
# one_nat_gateway_per_az: a NAT gateway in each zone's public subnet, and
# each private subnet routed through the one in its zone. It becomes a
# call of the module too.
resource "aws_vpc" "zonal" {
  cidr_block           = "10.61.0.0/16"
  enable_dns_hostnames = true
  tags                 = merge(local.tags, { Name = local.zonal })
}

resource "aws_internet_gateway" "zonal" {
  vpc_id = aws_vpc.zonal.id
  tags   = merge(local.tags, { Name = local.zonal })
}

resource "aws_subnet" "zonal_public" {
  for_each          = { for i, az in local.azs : az => "10.61.${i + 1}.0/24" }
  vpc_id            = aws_vpc.zonal.id
  availability_zone = each.key
  cidr_block        = each.value
  tags              = merge(local.tags, { Name = "${local.zonal}-public-${each.key}" })
}

resource "aws_subnet" "zonal_private" {
  for_each          = { for i, az in local.azs : az => "10.61.${i + 11}.0/24" }
  vpc_id            = aws_vpc.zonal.id
  availability_zone = each.key
  cidr_block        = each.value
  tags              = merge(local.tags, { Name = "${local.zonal}-private-${each.key}" })
}

resource "aws_eip" "zonal" {
  for_each = toset(local.azs)
  domain   = "vpc"
  tags     = merge(local.tags, { Name = "${local.zonal}-${each.key}" })
}

resource "aws_nat_gateway" "zonal" {
  for_each      = toset(local.azs)
  allocation_id = aws_eip.zonal[each.key].id
  subnet_id     = aws_subnet.zonal_public[each.key].id
  tags          = merge(local.tags, { Name = "${local.zonal}-${each.key}" })
  depends_on    = [aws_internet_gateway.zonal]
}

resource "aws_route_table" "zonal_public" {
  vpc_id = aws_vpc.zonal.id
  tags   = merge(local.tags, { Name = "${local.zonal}-public" })

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.zonal.id
  }
}

resource "aws_route_table" "zonal_private" {
  for_each = toset(local.azs)
  vpc_id   = aws_vpc.zonal.id
  tags     = merge(local.tags, { Name = "${local.zonal}-private-${each.key}" })

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.zonal[each.key].id
  }
}

resource "aws_route_table_association" "zonal_public" {
  for_each       = aws_subnet.zonal_public
  subnet_id      = each.value.id
  route_table_id = aws_route_table.zonal_public.id
}

resource "aws_route_table_association" "zonal_private" {
  for_each       = aws_subnet.zonal_private
  subnet_id      = each.value.id
  route_table_id = aws_route_table.zonal_private[each.key].id
}

# A security group with rules of their own, as the provider recommends.
resource "aws_security_group" "web" {
  name        = "${local.name}-web"
  description = "Web traffic"
  vpc_id      = aws_vpc.main.id
  tags        = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "https" {
  security_group_id = aws_security_group.web.id
  cidr_ipv4         = "0.0.0.0/0"
  from_port         = 443
  to_port           = 443
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.web.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# An instance role shaped like the iam-role module's, with an instance
# profile named after it.
data "aws_iam_policy_document" "ec2_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "app" {
  name               = "${local.name}-app"
  assume_role_policy = data.aws_iam_policy_document.ec2_trust.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "app_ssm" {
  role       = aws_iam_role.app.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "app" {
  name = "${local.name}-app"
  role = aws_iam_role.app.name
  tags = local.tags
}

# The smallest Graviton instance, in a private subnet, with no key pair.
data "aws_ssm_parameter" "al2023" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_instance" "app" {
  ami                    = nonsensitive(data.aws_ssm_parameter.al2023.value)
  instance_type          = "t4g.nano"
  subnet_id              = aws_subnet.private[local.azs[0]].id
  vpc_security_group_ids = [aws_security_group.web.id]
  iam_instance_profile   = aws_iam_instance_profile.app.name
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

# A type no lister lists, which the test lists through the query service
# (Terraform's list resources). It takes no tags and goes with its log
# group. The function logs no errors, so the metric gets no data, which
# is what CloudWatch charges for.
resource "aws_cloudwatch_log_metric_filter" "fn_errors" {
  name           = "${local.name}-fn-errors"
  log_group_name = aws_cloudwatch_log_group.fn.name
  pattern        = "ERROR"

  metric_transformation {
    name      = "${local.name}-fn-errors"
    namespace = "infraharvest-sandbox"
    value     = "1"
  }
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

# Data and messaging, at no standing cost.
resource "aws_kms_key" "app" {
  description             = "${local.name} app data"
  deletion_window_in_days = 7
  enable_key_rotation     = true
  tags                    = local.tags
}

resource "aws_kms_alias" "app" {
  name          = "alias/${local.name}-app"
  target_key_id = aws_kms_key.app.key_id
}

resource "aws_s3_bucket" "data" {
  bucket        = "${local.name}-${data.aws_caller_identity.current.account_id}-data"
  force_destroy = true
  tags          = local.tags
}

resource "aws_s3_bucket_versioning" "data" {
  bucket = aws_s3_bucket.data.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.app.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "data" {
  bucket                  = aws_s3_bucket.data.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_dynamodb_table" "items" {
  name         = "${local.name}-items"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  tags         = local.tags

  attribute {
    name = "id"
    type = "S"
  }
}

resource "aws_sqs_queue" "jobs" {
  name                      = "${local.name}-jobs"
  message_retention_seconds = 86400
  tags                      = local.tags
}

resource "aws_sns_topic" "events" {
  name = "${local.name}-events"
  tags = local.tags
}

resource "aws_sns_topic_subscription" "jobs" {
  topic_arn = aws_sns_topic.events.arn
  protocol  = "sqs"
  endpoint  = aws_sqs_queue.jobs.arn
}

resource "aws_ssm_parameter" "endpoint" {
  name  = "/${local.name}/endpoint"
  type  = "String"
  value = "https://api.example.com"
  tags  = local.tags
}

# A queue and a parameter in the second region, which an import of both
# regions keeps in that region's root.
resource "aws_sqs_queue" "second" {
  provider                  = aws.second
  name                      = "${local.name}-second"
  message_retention_seconds = 86400
  tags                      = local.tags
}

resource "aws_ssm_parameter" "second" {
  provider = aws.second
  name     = "/${local.name}/second"
  type     = "String"
  value    = var.second_region
  tags     = local.tags
}

# Created by a second apply, with extra = true, for an incremental import
# to add to the roots.
resource "aws_cloudwatch_log_group" "extra" {
  count             = var.extra ? 1 : 0
  name              = "/${local.name}/extra"
  retention_in_days = 1
  tags              = var.extra_name == "" ? local.tags : merge(local.tags, { Name = var.extra_name })
}

resource "aws_route53_zone" "internal" {
  name = "${local.name}.internal"
  tags = local.tags

  vpc {
    vpc_id = aws_vpc.main.id
  }
}

resource "aws_route53_record" "api" {
  zone_id = aws_route53_zone.internal.zone_id
  name    = "api.${local.name}.internal"
  type    = "A"
  ttl     = 300
  records = ["10.60.11.10"]
}

# An application load balancer with a target group and a listener, and an
# ECS service that runs no tasks.
resource "aws_lb" "web" {
  name               = "${local.name}-web"
  load_balancer_type = "application"
  internal           = false
  subnets            = [for s in aws_subnet.public : s.id]
  security_groups    = [aws_security_group.web.id]
  tags               = local.tags
}

resource "aws_lb_target_group" "web" {
  name        = "${local.name}-web"
  port        = 80
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.main.id
  tags        = local.tags
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.web.arn
  port              = 80
  protocol          = "HTTP"
  tags              = local.tags

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.web.arn
  }
}

resource "aws_ecs_cluster" "main" {
  name = local.name
  tags = local.tags
}

resource "aws_ecs_task_definition" "web" {
  family                   = "${local.name}-web"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  tags                     = local.tags

  container_definitions = jsonencode([{
    name      = "web"
    image     = "public.ecr.aws/nginx/nginx:stable"
    essential = true

    portMappings = [{
      containerPort = 80
      protocol      = "tcp"
    }]
  }])
}

resource "aws_ecs_service" "web" {
  name            = "${local.name}-web"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.web.arn
  launch_type     = "FARGATE"
  desired_count   = 0
  tags            = local.tags

  network_configuration {
    subnets         = [for s in aws_subnet.private : s.id]
    security_groups = [aws_security_group.web.id]
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.web.arn
    container_name   = "web"
    container_port   = 80
  }

  depends_on = [aws_lb_listener.http]
}

# An alarm on the queue's backlog, and a nightly EventBridge rule, both
# notifying the topic.
resource "aws_cloudwatch_metric_alarm" "jobs_backlog" {
  alarm_name          = "${local.name}-jobs-backlog"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.jobs.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 100
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.events.arn]
  tags                = local.tags
}

resource "aws_cloudwatch_event_rule" "nightly" {
  name                = "${local.name}-nightly"
  description         = "Every night at three"
  schedule_expression = "cron(0 3 * * ? *)"
  tags                = local.tags
}

resource "aws_cloudwatch_event_target" "nightly" {
  rule      = aws_cloudwatch_event_rule.nightly.name
  target_id = "events"
  arn       = aws_sns_topic.events.arn
}

# A Step Functions state machine with a role of its own, a secret with no
# value yet, and a stream of one shard.
data "aws_iam_policy_document" "states_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "workflow" {
  name               = "${local.name}-workflow"
  assume_role_policy = data.aws_iam_policy_document.states_trust.json
  tags               = local.tags
}

resource "aws_sfn_state_machine" "workflow" {
  name     = "${local.name}-workflow"
  role_arn = aws_iam_role.workflow.arn
  tags     = local.tags

  definition = jsonencode({
    Comment = "Does nothing"
    StartAt = "Done"
    States  = { Done = { Type = "Pass", End = true } }
  })
}

resource "aws_secretsmanager_secret" "db" {
  name        = "${local.name}/db"
  description = "Database credentials"
  # Deleted at once, so the next run can use the name again.
  recovery_window_in_days = 0
  tags                    = local.tags
}

resource "aws_kinesis_stream" "events" {
  name             = "${local.name}-events"
  shard_count      = 1
  retention_period = 24
  tags             = local.tags

  stream_mode_details {
    stream_mode = "PROVISIONED"
  }
}

# A REST API with one mock endpoint, deployed to a stage.
resource "aws_api_gateway_rest_api" "api" {
  name = "${local.name}-rest"
  tags = local.tags

  endpoint_configuration {
    types = ["REGIONAL"]
  }
}

resource "aws_api_gateway_resource" "health" {
  rest_api_id = aws_api_gateway_rest_api.api.id
  parent_id   = aws_api_gateway_rest_api.api.root_resource_id
  path_part   = "health"
}

resource "aws_api_gateway_method" "health" {
  rest_api_id   = aws_api_gateway_rest_api.api.id
  resource_id   = aws_api_gateway_resource.health.id
  http_method   = "GET"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "health" {
  rest_api_id       = aws_api_gateway_rest_api.api.id
  resource_id       = aws_api_gateway_resource.health.id
  http_method       = aws_api_gateway_method.health.http_method
  type              = "MOCK"
  request_templates = { "application/json" = jsonencode({ statusCode = 200 }) }
}

resource "aws_api_gateway_method_response" "health" {
  rest_api_id = aws_api_gateway_rest_api.api.id
  resource_id = aws_api_gateway_resource.health.id
  http_method = aws_api_gateway_method.health.http_method
  status_code = "200"
}

resource "aws_api_gateway_integration_response" "health" {
  rest_api_id = aws_api_gateway_rest_api.api.id
  resource_id = aws_api_gateway_resource.health.id
  http_method = aws_api_gateway_method.health.http_method
  status_code = aws_api_gateway_method_response.health.status_code
  depends_on  = [aws_api_gateway_integration.health]
}

resource "aws_api_gateway_deployment" "api" {
  rest_api_id = aws_api_gateway_rest_api.api.id
  depends_on  = [aws_api_gateway_integration_response.health]
}

resource "aws_api_gateway_stage" "live" {
  rest_api_id   = aws_api_gateway_rest_api.api.id
  deployment_id = aws_api_gateway_deployment.api.id
  stage_name    = "live"
  tags          = local.tags
}

# An HTTP API with a route to the function, deployed as it changes.
resource "aws_apigatewayv2_api" "http" {
  name          = "${local.name}-http"
  protocol_type = "HTTP"
  tags          = local.tags
}

resource "aws_apigatewayv2_integration" "fn" {
  api_id                 = aws_apigatewayv2_api.http.id
  integration_type       = "AWS_PROXY"
  integration_method     = "POST"
  integration_uri        = aws_lambda_function.fn.arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "hello" {
  api_id    = aws_apigatewayv2_api.http.id
  route_key = "GET /hello"
  target    = "integrations/${aws_apigatewayv2_integration.fn.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.http.id
  name        = "$default"
  auto_deploy = true
  tags        = local.tags
}

resource "aws_lambda_permission" "http" {
  statement_id  = "AllowHttpApi"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.fn.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.http.execution_arn}/*"
}

# A web ACL on the load balancer that blocks a list of addresses, and a
# CloudFront distribution in front of the load balancer.
resource "aws_wafv2_ip_set" "blocked" {
  name               = "${local.name}-blocked"
  scope              = "REGIONAL"
  ip_address_version = "IPV4"
  addresses          = ["192.0.2.0/24"]
  tags               = local.tags
}

resource "aws_wafv2_web_acl" "web" {
  name  = "${local.name}-web"
  scope = "REGIONAL"
  tags  = local.tags

  default_action {
    allow {}
  }

  rule {
    name     = "blocked"
    priority = 1

    action {
      block {}
    }

    statement {
      ip_set_reference_statement {
        arn = aws_wafv2_ip_set.blocked.arn
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = false
      metric_name                = "blocked"
      sampled_requests_enabled   = false
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = false
    metric_name                = "${local.name}-web"
    sampled_requests_enabled   = false
  }
}

resource "aws_wafv2_web_acl_association" "web" {
  resource_arn = aws_lb.web.arn
  web_acl_arn  = aws_wafv2_web_acl.web.arn
}

data "aws_cloudfront_cache_policy" "disabled" {
  name = "Managed-CachingDisabled"
}

resource "aws_cloudfront_distribution" "web" {
  enabled     = true
  comment     = local.name
  price_class = "PriceClass_100"
  tags        = local.tags
  # Created in the background: the import doesn't need it deployed.
  wait_for_deployment = false

  origin {
    domain_name = aws_lb.web.dns_name
    origin_id   = "web"

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "http-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  default_cache_behavior {
    target_origin_id       = "web"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    cache_policy_id        = data.aws_cloudfront_cache_policy.disabled.id
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = true
  }
}

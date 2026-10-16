# aws_ecs_service, from data/aws/ecs.yaml

import {
  to = aws_ecs_service.apps_web
  id = "apps/web"
}

import {
  to = aws_ecs_service.apps_worker
  id = "apps/worker"
}

# Left out of generated configuration: region, tags_all, iam_role when network_configuration, iam_role=/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS, deployment_configuration when deployment_configuration.strategy=ROLLING and deployment_configuration.bake_time_in_minutes=0 and !deployment_configuration.lifecycle_hook

# Left out where the plan stays the same, as the default: availability_zone_rebalancing=ENABLED, platform_version=LATEST

# Kept in state only, so a plan may set: wait_for_steady_state

# Generated configuration, without the arguments the catalogue omits:
resource "aws_ecs_service" "apps_web" {
  availability_zone_rebalancing = "ENABLED"
  cluster                       = "arn:aws:ecs:us-east-1:123456789012:cluster/apps"
  desired_count                 = 2
  launch_type                   = "FARGATE"
  name                          = "web"
  platform_version              = "LATEST"
  task_definition               = "web:3"
  load_balancer {
    container_name   = "web"
    container_port   = 8080
    target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/0123456789abcdef"
  }
  network_configuration {
    assign_public_ip = false
    security_groups  = ["sg-0a1b2c3d4e5f60718"]
    subnets          = ["subnet-0a1b2c3d4e5f60718"]
  }
}

resource "aws_ecs_service" "apps_worker" {
  cluster         = "arn:aws:ecs:us-east-1:123456789012:cluster/apps"
  desired_count   = 1
  iam_role        = "ecs-legacy-lb"
  launch_type     = "EC2"
  name            = "worker"
  task_definition = "worker:7"
  deployment_configuration {
    bake_time_in_minutes = "5"
    strategy             = "ROLLING"
  }
  load_balancer {
    container_name   = "worker"
    container_port   = 9000
    target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/worker/0123456789abcdef"
  }
}

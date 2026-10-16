resource "aws_ecs_service" "apps_web" {
  availability_zone_rebalancing = "ENABLED"
  cluster                       = "arn:aws:ecs:us-east-1:123456789012:cluster/apps"
  desired_count                 = 2
  iam_role                      = "/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS"
  launch_type                   = "FARGATE"
  name                          = "web"
  platform_version              = "LATEST"
  region                        = "us-east-1"
  task_definition               = "web:3"
  deployment_configuration {
    bake_time_in_minutes = "0"
    strategy             = "ROLLING"
  }
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
  region          = "us-east-1"
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

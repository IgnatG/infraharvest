resource "aws_ecs_task_definition" "worker" {
  container_definitions    = jsonencode([{ essential = true, image = "worker:7", memory = 512, name = "worker" }])
  enable_fault_injection   = false
  family                   = "worker"
  network_mode             = "bridge"
  region                   = "us-east-1"
  requires_compatibilities = ["EC2"]
}

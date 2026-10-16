# aws_ecs_task_definition, from data/aws/ecs.yaml

import {
  to = aws_ecs_task_definition.worker
  id = "arn:aws:ecs:us-east-1:123456789012:task-definition/worker:7"
}

# Left out where the plan stays the same, as the default: enable_fault_injection=false, network_mode=bridge

# Generated configuration, without the arguments the catalogue omits:
resource "aws_ecs_task_definition" "worker" {
  container_definitions    = jsonencode([{ essential = true, image = "worker:7", memory = 512, name = "worker" }])
  enable_fault_injection   = false
  family                   = "worker"
  network_mode             = "bridge"
  requires_compatibilities = ["EC2"]
}

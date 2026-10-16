# aws_ecs_task_definition, from data/aws/ecs.yaml

import {
  to = aws_ecs_task_definition.worker
  id = "arn:aws:ecs:us-east-1:123456789012:task-definition/worker:7"
}

# Left out of generated configuration: region, tags_all, enable_fault_injection=false, network_mode=bridge

# Generated configuration, without the arguments the catalogue omits:
resource "aws_ecs_task_definition" "worker" {
  container_definitions    = jsonencode([{ essential = true, image = "worker:7", memory = 512, name = "worker" }])
  family                   = "worker"
  requires_compatibilities = ["EC2"]
}

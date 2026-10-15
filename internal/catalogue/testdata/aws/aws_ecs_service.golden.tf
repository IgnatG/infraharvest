# aws_ecs_service, from data/aws/ecs.yaml

import {
  to = aws_ecs_service.apps_web
  id = "apps/web"
}

# Kept in state only, so a plan may set: wait_for_steady_state

# aws_autoscaling_group, from data/aws/auto_scaling.yaml

import {
  to = aws_autoscaling_group.web
  id = "web"
}

# Kept in state only, so a plan may set: force_delete, force_delete_warm_pool, ignore_failed_scaling_activities, wait_for_capacity_timeout

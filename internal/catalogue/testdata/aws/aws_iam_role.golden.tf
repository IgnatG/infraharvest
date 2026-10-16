# aws_iam_role, from data/aws/iam.yaml

import {
  to = aws_iam_role.app
  id = "app"
}

# AWSServiceRoleForECS: left out by default, would import as "AWSServiceRoleForECS": service-linked role, which AWS manages for a service

# How configuration refers to a resource the selection leaves out:
data "aws_iam_role" "app" {
  name = "app"
}

data "aws_iam_role" "ecs_service_linked" {
  name = "AWSServiceRoleForECS"
}

# Left out of generated configuration: region, tags_all, *.tags_all, inline_policy, managed_policy_arns

# Left out where the plan stays the same, as the default: max_session_duration=3600, path=/

# Kept in state only, so a plan may set: force_detach_policies

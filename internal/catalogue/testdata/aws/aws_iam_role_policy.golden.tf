# aws_iam_role_policy, from data/aws/iam.yaml

import {
  to = aws_iam_role_policy.app_read_queue
  id = "app:read-queue"
}

# AWSServiceRoleForECS:ECSServiceRolePolicy: left out by default, would import as "AWSServiceRoleForECS:ECSServiceRolePolicy": service-linked role, which AWS manages for a service

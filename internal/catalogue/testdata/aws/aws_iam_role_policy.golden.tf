# aws_iam_role_policy, from data/aws/iam.yaml

import {
  to = aws_iam_role_policy.app_read_queue
  id = "app:read-queue"
}

# AWSServiceRoleForECS:ECSServiceRolePolicy: left out by default, would import as "AWSServiceRoleForECS:ECSServiceRolePolicy": service-linked role, which AWS manages for a service
# AWSReservedSSO_DataAccess_0123456789abcdef:AwsSSOInlinePolicy: left out by default, would import as "AWSReservedSSO_DataAccess_0123456789abcdef:AwsSSOInlinePolicy": a role IAM Identity Center creates for a permission set, which only it can change

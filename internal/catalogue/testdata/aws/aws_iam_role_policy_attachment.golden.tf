# aws_iam_role_policy_attachment, from data/aws/iam.yaml

import {
  to = aws_iam_role_policy_attachment.app_ReadOnlyAccess
  id = "app/arn:aws:iam::aws:policy/ReadOnlyAccess"
}

# AWSServiceRoleForECS/arn:aws:iam::aws:policy/aws-service-role/AmazonECSServiceRolePolicy: left out by default, would import as "AWSServiceRoleForECS/arn:aws:iam::aws:policy/aws-service-role/AmazonECSServiceRolePolicy": service-linked role, which AWS manages for a service

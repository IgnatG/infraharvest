# aws_iam_role_policy_attachment, from data/aws/iam.yaml

import {
  to = aws_iam_role_policy_attachment.app_ReadOnlyAccess
  id = "app/arn:aws:iam::aws:policy/ReadOnlyAccess"
}

# AWSServiceRoleForECS/arn:aws:iam::aws:policy/aws-service-role/AmazonECSServiceRolePolicy: left out by default, would import as "AWSServiceRoleForECS/arn:aws:iam::aws:policy/aws-service-role/AmazonECSServiceRolePolicy": service-linked role, which AWS manages for a service
# AWSReservedSSO_AdministratorAccess_0123456789abcdef/arn:aws:iam::aws:policy/AdministratorAccess: left out by default, would import as "AWSReservedSSO_AdministratorAccess_0123456789abcdef/arn:aws:iam::aws:policy/AdministratorAccess": a role IAM Identity Center creates for a permission set, which only it can change

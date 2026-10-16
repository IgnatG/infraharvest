# aws_kms_key, from data/aws/kms.yaml

import {
  to = aws_kms_key.app
  id = "1234abcd-12ab-34cd-56ef-1234567890ab"
}

import {
  to = aws_kms_key.shared
  id = "0987dcba-09fe-87dc-65ba-ab0987654321"
}

# How configuration refers to a resource the selection leaves out:
data "aws_kms_key" "app" {
  key_id = "1234abcd-12ab-34cd-56ef-1234567890ab"
}

data "aws_kms_key" "shared" {
  key_id = "0987dcba-09fe-87dc-65ba-ab0987654321"
}

# Left out of generated configuration: region, tags_all, multi_region=false, rotation_period_in_days=365, policy={"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:{*}:iam::{*}:root"},"Action":"kms:*","Resource":"*"}]}

# Generated configuration, without the arguments the catalogue omits:
resource "aws_kms_key" "app" {
  bypass_policy_lockout_safety_check = false
  customer_master_key_spec           = "SYMMETRIC_DEFAULT"
  description                        = "app"
  enable_key_rotation                = true
  is_enabled                         = true
  key_usage                          = "ENCRYPT_DECRYPT"
}

resource "aws_kms_key" "shared" {
  description         = "shared with the logging account"
  enable_key_rotation = true
  multi_region        = true
  policy = jsonencode({
    Id = "key-default-1"
    Statement = [{
      Action = "kms:*"
      Effect = "Allow"
      Principal = {
        AWS = ["arn:aws:iam::123456789012:root", "arn:aws:iam::210987654321:root"]
      }
      Resource = "*"
      Sid      = "Enable IAM User Permissions"
    }]
    Version = "2012-10-17"
  })
  rotation_period_in_days = 90
}

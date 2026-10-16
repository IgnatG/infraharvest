resource "aws_kms_key" "app" {
  bypass_policy_lockout_safety_check = false
  customer_master_key_spec           = "SYMMETRIC_DEFAULT"
  description                        = "app"
  enable_key_rotation                = true
  is_enabled                         = true
  key_usage                          = "ENCRYPT_DECRYPT"
  multi_region                       = false
  policy = jsonencode({
    Id = "key-default-1"
    Statement = [{
      Action = "kms:*"
      Effect = "Allow"
      Principal = {
        AWS = "arn:aws:iam::123456789012:root"
      }
      Resource = "*"
      Sid      = "Enable IAM User Permissions"
    }]
    Version = "2012-10-17"
  })
  region                  = "eu-west-2"
  rotation_period_in_days = 365
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
  region                  = "eu-west-2"
  rotation_period_in_days = 90
}

# aws_iam_policy, from data/aws/iam.yaml

import {
  to = aws_iam_policy.app_read
  id = "arn:aws:iam::123456789012:policy/app-read"
}

# How configuration refers to a resource the selection leaves out:
data "aws_iam_policy" "app_read" {
  arn = "arn:aws:iam::123456789012:policy/app-read"
}

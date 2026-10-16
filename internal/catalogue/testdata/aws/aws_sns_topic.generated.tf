resource "aws_sns_topic" "alerts" {
  content_based_deduplication = false
  fifo_topic                  = false
  name                        = "alerts"
  policy = jsonencode({
    Id = "__default_policy_ID"
    Statement = [{
      Action = ["SNS:GetTopicAttributes", "SNS:SetTopicAttributes", "SNS:AddPermission", "SNS:RemovePermission", "SNS:DeleteTopic", "SNS:Subscribe", "SNS:ListSubscriptionsByTopic", "SNS:Publish"]
      Condition = {
        StringEquals = {
          "AWS:SourceOwner" = "123456789012"
        }
      }
      Effect = "Allow"
      Principal = {
        AWS = "*"
      }
      Resource = "arn:aws:sns:eu-west-2:123456789012:alerts"
      Sid      = "__default_statement_ID"
    }]
    Version = "2008-10-17"
  })
  region            = "eu-west-2"
  signature_version = 1
  tracing_config    = "PassThrough"
}

resource "aws_sns_topic" "events" {
  name = "events"
  policy = jsonencode({
    Statement = [{
      Action = "SNS:Publish"
      Effect = "Allow"
      Principal = {
        Service = "events.amazonaws.com"
      }
      Resource = "arn:aws:sns:eu-west-2:123456789012:events"
      Sid      = "events"
    }]
    Version = "2012-10-17"
  })
  region            = "eu-west-2"
  signature_version = 2
  tracing_config    = "Active"
}

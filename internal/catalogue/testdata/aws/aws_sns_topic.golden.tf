# aws_sns_topic, from data/aws/sns.yaml

import {
  to = aws_sns_topic.alerts
  id = "arn:aws:sns:eu-west-2:123456789012:alerts"
}

import {
  to = aws_sns_topic.events
  id = "arn:aws:sns:eu-west-2:123456789012:events"
}

# Left out of generated configuration: region, tags_all, *.tags_all, policy={"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Sid":"__default_statement_ID","Effect":"Allow","Principal":{"AWS":"*"},"Action":["SNS:GetTopicAttributes","SNS:SetTopicAttributes","SNS:AddPermission","SNS:RemovePermission","SNS:DeleteTopic","SNS:Subscribe","SNS:ListSubscriptionsByTopic","SNS:Publish"],"Resource":"arn:{*}:sns:{*}:{*}:{name}","Condition":{"StringEquals":{"AWS:SourceOwner":"{*}"}}}]}

# Left out where the plan stays the same, as the default: signature_version=1, tracing_config=PassThrough

# Generated configuration, without the arguments the catalogue omits:
resource "aws_sns_topic" "alerts" {
  content_based_deduplication = false
  fifo_topic                  = false
  name                        = "alerts"
  signature_version           = 1
  tracing_config              = "PassThrough"
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
  signature_version = 2
  tracing_config    = "Active"
}

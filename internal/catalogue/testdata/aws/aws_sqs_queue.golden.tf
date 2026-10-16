# aws_sqs_queue, from data/aws/sqs.yaml

import {
  to = aws_sqs_queue.orders
  id = "https://sqs.eu-west-2.amazonaws.com/123456789012/orders"
}

# Left out of generated configuration: region, tags_all, kms_data_key_reuse_period_seconds=300, sqs_managed_sse_enabled=true

# Generated configuration, without the arguments the catalogue omits:
resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  max_message_size           = 1048576
  message_retention_seconds  = 345600
  name                       = "orders"
  receive_wait_time_seconds  = 0
  visibility_timeout_seconds = 30
}

# aws_sfn_state_machine, from data/aws/sfn.yaml

import {
  to = aws_sfn_state_machine.orders
  id = "arn:aws:states:eu-west-2:123456789012:stateMachine:orders"
}

# Left out of generated configuration: region, tags_all, *.tags_all, encryption_configuration when encryption_configuration.type=AWS_OWNED_KEY, logging_configuration when logging_configuration.level=OFF and logging_configuration.include_execution_data=false, tracing_configuration when tracing_configuration.enabled=false

# Generated configuration, without the arguments the catalogue omits:
resource "aws_sfn_state_machine" "orders" {
  definition = jsonencode({ StartAt = "Done", States = { Done = { Type = "Succeed" } } })
  name       = "orders"
  publish    = false
  role_arn   = "arn:aws:iam::123456789012:role/orders-sfn"
  type       = "STANDARD"
}

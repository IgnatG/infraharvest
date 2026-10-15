# aws_cloudwatch_event_bus, from data/aws/eventbridge.yaml

import {
  to = aws_cloudwatch_event_bus.orders
  id = "orders"
}

# default: left out by default, would import as "default": the default event bus, which AWS creates for the account

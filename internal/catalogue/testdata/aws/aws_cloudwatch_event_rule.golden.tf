# aws_cloudwatch_event_rule, from data/aws/cloudwatch.yaml

import {
  to = aws_cloudwatch_event_rule.nightly
  id = "default/nightly"
}

# Left out of generated configuration: region, tags_all, *.tags_all, is_enabled

# Left out where the plan stays the same, as the default: event_bus_name=default, state=ENABLED

# Generated configuration, without the arguments the catalogue omits:
resource "aws_cloudwatch_event_rule" "nightly" {
  event_bus_name      = "default"
  force_destroy       = false
  name                = "nightly"
  schedule_expression = "cron(0 2 * * ? *)"
  state               = "ENABLED"
}

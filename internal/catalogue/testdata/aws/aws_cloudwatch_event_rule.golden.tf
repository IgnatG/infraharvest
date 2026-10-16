# aws_cloudwatch_event_rule, from data/aws/cloudwatch.yaml

import {
  to = aws_cloudwatch_event_rule.nightly
  id = "default/nightly"
}

# Left out of generated configuration: region, tags_all, is_enabled, state=ENABLED

# Generated configuration, without the arguments the catalogue omits:
resource "aws_cloudwatch_event_rule" "nightly" {
  event_bus_name      = "default"
  force_destroy       = false
  name                = "nightly"
  schedule_expression = "cron(0 2 * * ? *)"
}

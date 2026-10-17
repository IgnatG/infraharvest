# aws_cloudwatch_metric_alarm, from data/aws/cloudwatch.yaml

import {
  to = aws_cloudwatch_metric_alarm.api_5xx
  id = "api-5xx"
}

# TargetTracking-table/orders-AlarmHigh-1f2e3d4c-5b6a-7980-a1b2-c3d4e5f60718: left out by default, would import as "TargetTracking-table/orders-AlarmHigh-1f2e3d4c-5b6a-7980-a1b2-c3d4e5f60718": an alarm a target tracking scaling policy creates and manages

# Left out where the plan stays the same, as the default: actions_enabled=true, treat_missing_data=missing

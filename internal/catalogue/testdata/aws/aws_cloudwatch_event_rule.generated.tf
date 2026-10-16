resource "aws_cloudwatch_event_rule" "nightly" {
  event_bus_name      = "default"
  force_destroy       = false
  is_enabled          = true
  name                = "nightly"
  region              = "eu-west-2"
  schedule_expression = "cron(0 2 * * ? *)"
  state               = "ENABLED"
}

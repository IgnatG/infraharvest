resource "aws_sfn_state_machine" "orders" {
  definition = jsonencode({ StartAt = "Done", States = { Done = { Type = "Succeed" } } })
  name       = "orders"
  publish    = false
  region     = "eu-west-2"
  role_arn   = "arn:aws:iam::123456789012:role/orders-sfn"
  type       = "STANDARD"
  encryption_configuration {
    kms_data_key_reuse_period_seconds = 0
    kms_key_id                        = ""
    type                              = "AWS_OWNED_KEY"
  }
  logging_configuration {
    include_execution_data = false
    level                  = "OFF"
    log_destination        = ""
  }
  tracing_configuration {
    enabled = false
  }
}

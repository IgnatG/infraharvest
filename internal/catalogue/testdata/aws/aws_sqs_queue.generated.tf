resource "aws_sqs_queue" "orders" {
  delay_seconds                     = 0
  kms_data_key_reuse_period_seconds = 300
  max_message_size                  = 1048576
  message_retention_seconds         = 345600
  name                              = "orders"
  receive_wait_time_seconds         = 0
  region                            = "eu-west-2"
  sqs_managed_sse_enabled           = true
  visibility_timeout_seconds        = 30
}

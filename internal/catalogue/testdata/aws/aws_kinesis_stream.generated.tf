resource "aws_kinesis_stream" "clicks" {
  arn                       = "arn:aws:kinesis:eu-west-2:123456789012:stream/clicks"
  encryption_type           = "NONE"
  enforce_consumer_deletion = false
  max_record_size_in_kib    = 1024
  name                      = "clicks"
  region                    = "eu-west-2"
  retention_period          = 24
  shard_count               = 2
  stream_mode_details {
    stream_mode = "PROVISIONED"
  }
}

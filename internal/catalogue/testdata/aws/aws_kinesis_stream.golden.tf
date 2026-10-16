# aws_kinesis_stream, from data/aws/kinesis.yaml

import {
  to = aws_kinesis_stream.clicks
  id = "arn:aws:kinesis:eu-west-2:123456789012:stream/clicks"
}

# Left out of generated configuration: region, tags_all, *.tags_all, arn, stream_mode_details when stream_mode_details.stream_mode=PROVISIONED

# Left out where the plan stays the same, as the default: max_record_size_in_kib=1024, retention_period=24

# Generated configuration, without the arguments the catalogue omits:
resource "aws_kinesis_stream" "clicks" {
  encryption_type           = "NONE"
  enforce_consumer_deletion = false
  max_record_size_in_kib    = 1024
  name                      = "clicks"
  retention_period          = 24
  shard_count               = 2
}

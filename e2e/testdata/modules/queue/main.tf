# An organisation's own queue module, which the AWS end-to-end test lists
# in its configuration file: the queue takes each setting from a variable.

terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.0"
    }
  }
}

resource "aws_sqs_queue" "this" {
  name                              = var.name
  content_based_deduplication       = var.content_based_deduplication
  deduplication_scope               = var.deduplication_scope
  delay_seconds                     = var.delay_seconds
  fifo_queue                        = var.fifo_queue
  fifo_throughput_limit             = var.fifo_throughput_limit
  kms_data_key_reuse_period_seconds = var.kms_data_key_reuse_period_seconds
  kms_master_key_id                 = var.kms_master_key_id
  max_message_size                  = var.max_message_size
  message_retention_seconds         = var.message_retention_seconds
  policy                            = var.policy
  receive_wait_time_seconds         = var.receive_wait_time_seconds
  redrive_allow_policy              = var.redrive_allow_policy
  redrive_policy                    = var.redrive_policy
  sqs_managed_sse_enabled           = var.sqs_managed_sse_enabled
  tags                              = var.tags
  visibility_timeout_seconds        = var.visibility_timeout_seconds
}

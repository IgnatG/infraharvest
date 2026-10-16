# aws_s3_bucket, from data/aws/s3.yaml

import {
  to = aws_s3_bucket.app_logs
  id = "app-logs"
}

# How configuration refers to a resource the selection leaves out:
data "aws_s3_bucket" "app_logs" {
  bucket = "app-logs"
}

# Imported with each resource, where it has them, with its import ID:
#   aws_s3_bucket_versioning
#   aws_s3_bucket_server_side_encryption_configuration
#   aws_s3_bucket_lifecycle_configuration
#   aws_s3_bucket_cors_configuration
#   aws_s3_bucket_website_configuration
#   aws_s3_bucket_logging
#   aws_s3_bucket_public_access_block
#   aws_s3_bucket_ownership_controls
#   aws_s3_bucket_accelerate_configuration
#   aws_s3_bucket_request_payment_configuration
#   aws_s3_bucket_object_lock_configuration
#   aws_s3_bucket_replication_configuration

# Left out of generated configuration: region, tags_all, *.tags_all, acceleration_status, acl, cors_rule, grant, lifecycle_rule, logging, object_lock_configuration, policy, replication_configuration, request_payer, server_side_encryption_configuration, versioning, website

# Generated configuration, without the arguments the catalogue omits:
# __generated__ by Terraform
resource "aws_s3_bucket" "app_logs" {
  bucket              = "app-logs"
  force_destroy       = false
  object_lock_enabled = false
  tags                = {}
}

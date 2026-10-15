# __generated__ by Terraform
resource "aws_s3_bucket" "app_logs" {
  bucket              = "app-logs"
  force_destroy       = false
  object_lock_enabled = false
  region              = "us-east-1"
  tags                = {}
  tags_all            = {}
  versioning {
    enabled    = true
    mfa_delete = false
  }
  server_side_encryption_configuration {
    rule {
      bucket_key_enabled = false
      apply_server_side_encryption_by_default {
        kms_master_key_id = null
        sse_algorithm     = "AES256"
      }
    }
  }
}

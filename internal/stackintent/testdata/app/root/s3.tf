resource "aws_s3_bucket" "app_data" {
  bucket = "app-data-123"
  tags = {
    env = "prod"
  }
}

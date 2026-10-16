resource "aws_dynamodb_table" "orders" {
  billing_mode   = "PAY_PER_REQUEST"
  hash_key       = "id"
  name           = "orders"
  read_capacity  = 0
  region         = "eu-west-2"
  table_class    = "STANDARD"
  write_capacity = 0
  attribute {
    name = "id"
    type = "S"
  }
  attribute {
    name = "customer"
    type = "S"
  }
  global_secondary_index {
    hash_key        = "customer"
    name            = "by-customer"
    projection_type = "ALL"
    read_capacity   = 0
    write_capacity  = 0
  }
  point_in_time_recovery {
    enabled                 = false
    recovery_period_in_days = 0
  }
  server_side_encryption {
    enabled = false
  }
  ttl {
    attribute_name = ""
    enabled        = false
  }
}

resource "aws_dynamodb_table" "sessions" {
  billing_mode   = "PROVISIONED"
  hash_key       = "id"
  name           = "sessions"
  read_capacity  = 5
  region         = "eu-west-2"
  write_capacity = 5
  attribute {
    name = "id"
    type = "S"
  }
  point_in_time_recovery {
    enabled                 = true
    recovery_period_in_days = 35
  }
  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }
}

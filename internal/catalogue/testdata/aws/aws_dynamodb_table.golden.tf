# aws_dynamodb_table, from data/aws/dynamodb.yaml

import {
  to = aws_dynamodb_table.orders
  id = "orders"
}

import {
  to = aws_dynamodb_table.sessions
  id = "sessions"
}

# Left out of generated configuration: region, tags_all, read_capacity=0 when billing_mode=PAY_PER_REQUEST, write_capacity=0 when billing_mode=PAY_PER_REQUEST, global_secondary_index.read_capacity=0 when billing_mode=PAY_PER_REQUEST, global_secondary_index.write_capacity=0 when billing_mode=PAY_PER_REQUEST, point_in_time_recovery when point_in_time_recovery.enabled=false, server_side_encryption when server_side_encryption.enabled=false, ttl when ttl.enabled=false

# Left out where the plan stays the same, as the default: table_class=STANDARD

# Generated configuration, without the arguments the catalogue omits:
resource "aws_dynamodb_table" "orders" {
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  name         = "orders"
  table_class  = "STANDARD"
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
  }
}

resource "aws_dynamodb_table" "sessions" {
  billing_mode   = "PROVISIONED"
  hash_key       = "id"
  name           = "sessions"
  read_capacity  = 5
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

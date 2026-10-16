resource "aws_lambda_function" "orders" {
  architectures                  = ["x86_64"]
  code_sha256                    = "vDXb0eJ2b8sZkxzLy1e6N8yHqP0n2QGJ7m+u0gP1R4s="
  function_name                  = "orders"
  handler                        = "index.handler"
  memory_size                    = 128
  package_type                   = "Zip"
  region                         = "eu-west-2"
  reserved_concurrent_executions = -1
  role                           = "arn:aws:iam::123456789012:role/orders"
  runtime                        = "nodejs22.x"
  timeout                        = 3
  ephemeral_storage {
    size = 512
  }
  logging_config {
    application_log_level = ""
    log_format            = "Text"
    log_group             = "/aws/lambda/orders"
    system_log_level      = ""
  }
  tracing_config {
    mode = "PassThrough"
  }
}

# aws_lambda_function, from data/aws/lambda.yaml

import {
  to = aws_lambda_function.orders
  id = "orders"
}

# Left out of generated configuration: region, tags_all, code_sha256, logging_config when logging_config.log_format=Text and logging_config.log_group=/aws/lambda/{function_name}, tracing_config when tracing_config.mode=PassThrough

# Left out where the plan stays the same, as the default: architectures=["x86_64"], ephemeral_storage.size=512, memory_size=128, package_type=Zip, reserved_concurrent_executions=-1, timeout=3

# Without any of filename, image_uri, s3_bucket: placeholders, ignored by Terraform (the function's code is deployed outside Terraform; filename is a placeholder. To deploy it with Terraform, put the package there, set code_sha256 to its filebase64sha256, which is what makes the provider update the code when the package changes, and remove this):
#   filename = "lambda/{function_name}.zip"
#   lifecycle.ignore_changes = [filename, code_sha256]

# Generated configuration, without the arguments the catalogue omits:
resource "aws_lambda_function" "orders" {
  architectures                  = ["x86_64"]
  function_name                  = "orders"
  handler                        = "index.handler"
  memory_size                    = 128
  package_type                   = "Zip"
  reserved_concurrent_executions = -1
  role                           = "arn:aws:iam::123456789012:role/orders"
  runtime                        = "nodejs22.x"
  timeout                        = 3
  ephemeral_storage {
    size = 512
  }
}

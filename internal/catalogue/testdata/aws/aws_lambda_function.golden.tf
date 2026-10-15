# aws_lambda_function, from data/aws/lambda.yaml

import {
  to = aws_lambda_function.orders
  id = "orders"
}

# Without any of filename, image_uri, s3_bucket: placeholders, ignored by Terraform (the function's code is deployed outside Terraform; filename is a placeholder. To deploy it with Terraform, put the package there, set source_code_hash and remove this):
#   filename = "lambda/{function_name}.zip"
#   lifecycle.ignore_changes = [filename, source_code_hash]

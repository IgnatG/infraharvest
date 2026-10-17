# aws_lambda_layer_version, from data/aws/lambda.yaml

import {
  to = aws_lambda_layer_version.deps_3
  id = "arn:aws:lambda:eu-west-2:123456789012:layer:deps:3"
}

# Without any of filename, s3_bucket: placeholders, ignored by Terraform (the layer's code was published outside Terraform; filename is a placeholder. To publish a new version with Terraform, put the package there, set source_code_hash to its filebase64sha256, and remove this):
#   filename = "lambda/layers/{layer_name}.zip"
#   lifecycle.ignore_changes = [filename, source_code_hash]

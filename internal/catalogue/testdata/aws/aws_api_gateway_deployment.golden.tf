# aws_api_gateway_deployment, from data/aws/api_gateway.yaml

import {
  to = aws_api_gateway_deployment.shop_prod
  id = "a1b2c3d4e5/abc123"
}

# Every resource gets lifecycle.create_before_destroy = true (a stage refers to its deployment, so a new one must exist before the old one goes. To redeploy when the API changes, add triggers = { redeployment = sha1(jsonencode([...])) } listing the IDs of its resources, methods and integrations; the next apply then replaces this deployment once)

# Generated configuration, without the arguments the catalogue omits:
resource "aws_api_gateway_deployment" "shop_prod" {
  description = ""
  rest_api_id = "a1b2c3d4e5"
}

# aws_api_gateway_integration, from data/aws/api_gateway.yaml

import {
  to = aws_api_gateway_integration.shop_orders_get
  id = "a1b2c3d4e5/xyz789/GET"
}

# Left out of generated configuration: region, tags_all, *.tags_all, cache_namespace={resource_id}

# Left out where the plan stays the same, as the default: connection_type=INTERNET, passthrough_behavior=WHEN_NO_MATCH, response_transfer_mode=BUFFERED, timeout_milliseconds=29000

# Generated configuration, without the arguments the catalogue omits:
resource "aws_api_gateway_integration" "shop_orders_get" {
  cache_key_parameters    = []
  connection_type         = "INTERNET"
  content_handling        = ""
  http_method             = "GET"
  integration_http_method = "POST"
  passthrough_behavior    = "WHEN_NO_MATCH"
  resource_id             = "xyz789"
  response_transfer_mode  = "BUFFERED"
  rest_api_id             = "a1b2c3d4e5"
  timeout_milliseconds    = 29000
  type                    = "AWS_PROXY"
  uri                     = "arn:aws:apigateway:eu-west-2:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-2:123456789012:function:orders/invocations"
}

resource "aws_api_gateway_integration" "shop_orders_get" {
  cache_key_parameters    = []
  cache_namespace         = "xyz789"
  connection_type         = "INTERNET"
  content_handling        = ""
  http_method             = "GET"
  integration_http_method = "POST"
  passthrough_behavior    = "WHEN_NO_MATCH"
  region                  = "eu-west-2"
  resource_id             = "xyz789"
  response_transfer_mode  = "BUFFERED"
  rest_api_id             = "a1b2c3d4e5"
  timeout_milliseconds    = 29000
  type                    = "AWS_PROXY"
  uri                     = "arn:aws:apigateway:eu-west-2:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-2:123456789012:function:orders/invocations"
}

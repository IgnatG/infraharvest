resource "aws_apigatewayv2_api" "orders" {
  api_key_selection_expression = "$request.header.x-api-key"
  ip_address_type              = "ipv4"
  name                         = "orders"
  protocol_type                = "HTTP"
  region                       = "eu-west-2"
  route_selection_expression   = "$request.method $request.path"
}

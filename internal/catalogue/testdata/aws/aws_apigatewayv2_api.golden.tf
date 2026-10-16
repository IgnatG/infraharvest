# aws_apigatewayv2_api, from data/aws/api_gatewayv2.yaml

import {
  to = aws_apigatewayv2_api.orders
  id = "h1j2k3l4m5"
}

# Left out where the plan stays the same, as the default: api_key_selection_expression=$request.header.x-api-key, ip_address_type=ipv4, route_selection_expression=$request.method $request.path

# Generated configuration, without the arguments the catalogue omits:
resource "aws_apigatewayv2_api" "orders" {
  api_key_selection_expression = "$request.header.x-api-key"
  ip_address_type              = "ipv4"
  name                         = "orders"
  protocol_type                = "HTTP"
  route_selection_expression   = "$request.method $request.path"
}

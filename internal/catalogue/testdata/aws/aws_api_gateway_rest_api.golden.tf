# aws_api_gateway_rest_api, from data/aws/api_gateway.yaml

import {
  to = aws_api_gateway_rest_api.shop
  id = "a1b2c3d4e5"
}

# Left out of generated configuration: region, tags_all, *.tags_all, endpoint_configuration when endpoint_configuration.types=["EDGE"] and endpoint_configuration.ip_address_type=ipv4

# Left out where the plan stays the same, as the default: api_key_source=HEADER, disable_execute_api_endpoint=false, put_rest_api_mode=overwrite, security_policy=TLS_1_0

# Generated configuration, without the arguments the catalogue omits:
resource "aws_api_gateway_rest_api" "shop" {
  api_key_source               = "HEADER"
  binary_media_types           = []
  description                  = ""
  disable_execute_api_endpoint = false
  name                         = "shop"
  put_rest_api_mode            = "overwrite"
  security_policy              = "TLS_1_0"
}

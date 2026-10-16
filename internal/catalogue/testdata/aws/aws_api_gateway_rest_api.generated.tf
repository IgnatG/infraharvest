resource "aws_api_gateway_rest_api" "shop" {
  api_key_source               = "HEADER"
  binary_media_types           = []
  description                  = ""
  disable_execute_api_endpoint = false
  name                         = "shop"
  put_rest_api_mode            = "overwrite"
  region                       = "eu-west-2"
  security_policy              = "TLS_1_0"
  endpoint_configuration {
    ip_address_type = "ipv4"
    types           = ["EDGE"]
  }
}

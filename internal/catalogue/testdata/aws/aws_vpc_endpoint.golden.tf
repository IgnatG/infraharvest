# aws_vpc_endpoint, from data/aws/vpc_endpoint.yaml

import {
  to = aws_vpc_endpoint.s3
  id = "vpce-0a1b2c3d4e5f60718"
}

import {
  to = aws_vpc_endpoint.ssm
  id = "vpce-0f1e2d3c4b5a69788"
}

# How configuration refers to a resource the selection leaves out:
data "aws_vpc_endpoint" "s3" {
  id = "vpce-0a1b2c3d4e5f60718"
}

data "aws_vpc_endpoint" "ssm" {
  id = "vpce-0f1e2d3c4b5a69788"
}

# Left out of generated configuration: region, tags_all, *.tags_all, dns_options when vpc_endpoint_type=Gateway, ip_address_type=ipv4 when vpc_endpoint_type=Gateway, private_dns_enabled=false when vpc_endpoint_type=Gateway, security_group_ids when vpc_endpoint_type=Gateway, subnet_configuration when vpc_endpoint_type=Gateway, subnet_ids when vpc_endpoint_type=Gateway, policy={"Version":"2008-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"*","Resource":"*"}]}, policy={"Statement":[{"Effect":"Allow","Principal":"*","Action":"*","Resource":"*"}]}

# Generated configuration, without the arguments the catalogue omits:
resource "aws_vpc_endpoint" "s3" {
  route_table_ids   = ["rtb-0a1b2c3d4e5f60718"]
  service_name      = "com.amazonaws.eu-west-2.s3"
  vpc_endpoint_type = "Gateway"
  vpc_id            = "vpc-0a1b2c3d4e5f60718"
}

resource "aws_vpc_endpoint" "ssm" {
  ip_address_type     = "ipv4"
  private_dns_enabled = true
  security_group_ids  = ["sg-0a1b2c3d4e5f60718"]
  service_name        = "com.amazonaws.eu-west-2.ssm"
  subnet_ids          = ["subnet-0a1b2c3d4e5f60718"]
  vpc_endpoint_type   = "Interface"
  vpc_id              = "vpc-0a1b2c3d4e5f60718"
  dns_options {
    dns_record_ip_type                             = "ipv4"
    private_dns_only_for_inbound_resolver_endpoint = false
  }
}

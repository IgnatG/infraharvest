resource "aws_vpc_endpoint" "s3" {
  ip_address_type = "ipv4"
  policy = jsonencode({
    Statement = [{
      Action    = "*"
      Effect    = "Allow"
      Principal = "*"
      Resource  = "*"
    }]
    Version = "2008-10-17"
  })
  private_dns_enabled = false
  region              = "eu-west-2"
  route_table_ids     = ["rtb-0a1b2c3d4e5f60718"]
  security_group_ids  = []
  service_name        = "com.amazonaws.eu-west-2.s3"
  subnet_ids          = []
  vpc_endpoint_type   = "Gateway"
  vpc_id              = "vpc-0a1b2c3d4e5f60718"
  dns_options {
    dns_record_ip_type                             = "ipv4"
    private_dns_only_for_inbound_resolver_endpoint = false
  }
}

resource "aws_vpc_endpoint" "ssm" {
  ip_address_type = "ipv4"
  policy = jsonencode({
    Statement = [{
      Action    = "*"
      Effect    = "Allow"
      Principal = "*"
      Resource  = "*"
    }]
  })
  private_dns_enabled = true
  region              = "eu-west-2"
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

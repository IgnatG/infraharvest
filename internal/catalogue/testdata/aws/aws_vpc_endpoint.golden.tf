# aws_vpc_endpoint, from data/aws/vpc_endpoint.yaml

import {
  to = aws_vpc_endpoint.s3
  id = "vpce-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_vpc_endpoint" "s3" {
  id = "vpce-0a1b2c3d4e5f60718"
}

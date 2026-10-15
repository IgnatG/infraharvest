# aws_internet_gateway, from data/aws/igw.yaml

import {
  to = aws_internet_gateway.main
  id = "igw-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_internet_gateway" "main" {
  internet_gateway_id = "igw-0a1b2c3d4e5f60718"
}

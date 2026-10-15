# aws_nat_gateway, from data/aws/nat.yaml

import {
  to = aws_nat_gateway.public_a
  id = "nat-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_nat_gateway" "public_a" {
  id = "nat-0a1b2c3d4e5f60718"
}

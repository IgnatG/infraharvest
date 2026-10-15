# aws_vpc, from data/aws/vpc.yaml

import {
  to = aws_vpc.main
  id = "vpc-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_vpc" "main" {
  id = "vpc-0a1b2c3d4e5f60718"
}

# aws_subnet, from data/aws/subnet.yaml

import {
  to = aws_subnet.private_a
  id = "subnet-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_subnet" "private_a" {
  id = "subnet-0a1b2c3d4e5f60718"
}

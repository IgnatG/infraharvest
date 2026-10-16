# aws_eip, from data/aws/eip.yaml

import {
  to = aws_eip.edge
  id = "eipalloc-0f1e2d3c4b5a69788"
}

import {
  to = aws_eip.nat
  id = "eipalloc-0a1b2c3d4e5f60718"
}

# Left out of generated configuration: region, tags_all, domain=vpc, network_border_group={region}, public_ipv4_pool=amazon

# Generated configuration, without the arguments the catalogue omits:
resource "aws_eip" "nat" {
  tags = {
    Name = "nat"
  }
}

resource "aws_eip" "edge" {
  network_border_group = "us-west-2-lax-1"
  public_ipv4_pool     = "ipv4pool-ec2-0a1b2c3d4e5f60718"
}

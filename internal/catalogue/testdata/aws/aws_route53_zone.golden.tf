# aws_route53_zone, from data/aws/route53.yaml

import {
  to = aws_route53_zone.internal
  id = "Z0123456789ABCDEFGHIJ"
}

# How configuration refers to a resource the selection leaves out:
data "aws_route53_zone" "internal" {
  zone_id = "Z0123456789ABCDEFGHIJ"
}

# Left out where the plan stays the same, as the default: comment=Managed by Terraform, enable_accelerated_recovery=false

# Generated configuration, without the arguments the catalogue omits:
resource "aws_route53_zone" "internal" {
  comment                     = "Managed by Terraform"
  enable_accelerated_recovery = false
  force_destroy               = false
  name                        = "example.internal"
  vpc {
    vpc_id     = "vpc-0a1b2c3d4e5f60718"
    vpc_region = "eu-west-2"
  }
}

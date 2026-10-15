# aws_route53_zone, from data/aws/route53.yaml

import {
  to = aws_route53_zone.internal
  id = "Z0123456789ABCDEFGHIJ"
}

# How configuration refers to a resource the selection leaves out:
data "aws_route53_zone" "internal" {
  zone_id = "Z0123456789ABCDEFGHIJ"
}

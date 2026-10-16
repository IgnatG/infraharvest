# aws_lb, from data/aws/alb.yaml

import {
  to = aws_lb.web
  id = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/web/0123456789abcdef"
}

# Left out where the plan stays the same, as the default: enable_prefix_for_ipv6_source_nat=off, ip_address_type=ipv4

# Generated configuration, without the arguments the catalogue omits:
resource "aws_lb" "web" {
  client_keep_alive                 = 3600
  desync_mitigation_mode            = "defensive"
  drop_invalid_header_fields        = false
  enable_deletion_protection        = false
  enable_http2                      = true
  enable_prefix_for_ipv6_source_nat = "off"
  idle_timeout                      = 60
  internal                          = false
  ip_address_type                   = "ipv4"
  load_balancer_type                = "application"
  name                              = "web"
  security_groups                   = ["sg-0a1b2c3d4e5f60718"]
  subnets                           = ["subnet-0a1b2c3d4e5f60718", "subnet-0f1e2d3c4b5a69788"]
  xff_header_processing_mode        = "append"
}

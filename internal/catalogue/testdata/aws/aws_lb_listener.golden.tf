# aws_lb_listener, from data/aws/alb.yaml

import {
  to = aws_lb_listener.web_http
  id = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:listener/app/web/0123456789abcdef/fedcba9876543210"
}

# Left out of generated configuration: region, tags_all, *.tags_all, mutual_authentication when mutual_authentication.mode=off

# Left out where the plan stays the same, as the default: default_action.order=1, routing_http_response_server_enabled=true, tcp_idle_timeout_seconds=350

# Generated configuration, without the arguments the catalogue omits:
resource "aws_lb_listener" "web_http" {
  load_balancer_arn                    = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/web/0123456789abcdef"
  port                                 = 80
  protocol                             = "HTTP"
  routing_http_response_server_enabled = true
  default_action {
    order            = 1
    target_group_arn = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:targetgroup/web/0123456789abcdef"
    type             = "forward"
    forward {
      target_group {
        arn    = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:targetgroup/web/0123456789abcdef"
        weight = 1
      }
      stickiness {
        duration = 3600
        enabled  = false
      }
    }
  }
}

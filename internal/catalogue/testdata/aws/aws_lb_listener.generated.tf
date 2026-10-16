resource "aws_lb_listener" "web_http" {
  load_balancer_arn                    = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/web/0123456789abcdef"
  port                                 = 80
  protocol                             = "HTTP"
  region                               = "eu-west-2"
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
  mutual_authentication {
    mode = "off"
  }
}

resource "aws_lb_target_group" "web" {
  connection_termination            = false
  deregistration_delay              = "300"
  ip_address_type                   = "ipv4"
  load_balancing_algorithm_type     = "round_robin"
  load_balancing_anomaly_mitigation = "off"
  load_balancing_cross_zone_enabled = "use_load_balancer_configuration"
  name                              = "web"
  port                              = 80
  protocol                          = "HTTP"
  protocol_version                  = "HTTP1"
  region                            = "us-east-1"
  slow_start                        = 0
  target_type                       = "instance"
  vpc_id                            = "vpc-0a1b2c3d4e5f60718"
  health_check {
    enabled             = true
    healthy_threshold   = 5
    interval            = 30
    matcher             = "200"
    path                = "/health"
    port                = "traffic-port"
    protocol            = "HTTP"
    timeout             = 5
    unhealthy_threshold = 2
  }
  stickiness {
    cookie_duration = 86400
    enabled         = false
    type            = "lb_cookie"
  }
  target_group_health {
    dns_failover {
      minimum_healthy_targets_count      = "off"
      minimum_healthy_targets_percentage = "off"
    }
    unhealthy_state_routing {
      minimum_healthy_targets_count      = 1
      minimum_healthy_targets_percentage = "off"
    }
  }
}

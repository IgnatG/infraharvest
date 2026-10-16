# aws_lb_target_group, from data/aws/alb.yaml

import {
  to = aws_lb_target_group.web
  id = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/0123456789abcdef"
}

# Left out of generated configuration: region, tags_all, connection_termination=false, ip_address_type=ipv4, load_balancing_algorithm_type=round_robin, load_balancing_anomaly_mitigation=off, load_balancing_cross_zone_enabled=use_load_balancer_configuration, protocol_version=HTTP1, stickiness when stickiness.enabled=false, target_group_health when target_group_health.dns_failover.minimum_healthy_targets_count=off and target_group_health.dns_failover.minimum_healthy_targets_percentage=off and target_group_health.unhealthy_state_routing.minimum_healthy_targets_count=1 and target_group_health.unhealthy_state_routing.minimum_healthy_targets_percentage=off

# Kept in state only, so a plan may set: lambda_multi_value_headers_enabled, proxy_protocol_v2

# Generated configuration, without the arguments the catalogue omits:
resource "aws_lb_target_group" "web" {
  deregistration_delay = "300"
  name                 = "web"
  port                 = 80
  protocol             = "HTTP"
  slow_start           = 0
  target_type          = "instance"
  vpc_id               = "vpc-0a1b2c3d4e5f60718"
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
}

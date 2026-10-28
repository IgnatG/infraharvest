# aws_route53_health_check, from data/aws/route53.yaml

import {
  to = aws_route53_health_check.example_com_HTTPS
  id = "0b1c2d3e-0000-4000-8000-000000000001"
}

# 0b1c2d3e-0000-4000-8000-000000000002: left out by default, would import as "0b1c2d3e-0000-4000-8000-000000000002": a health check another AWS service created, which only that service can change; select it to import it

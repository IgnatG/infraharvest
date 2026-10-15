# aws_elasticache_subnet_group, from data/aws/elasticache.yaml

import {
  to = aws_elasticache_subnet_group.sessions
  id = "sessions"
}

# default: left out by default, would import as "default": the default cache subnet group, which ElastiCache creates for the default VPC

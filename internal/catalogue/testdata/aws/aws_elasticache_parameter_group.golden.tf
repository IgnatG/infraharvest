# aws_elasticache_parameter_group, from data/aws/elasticache.yaml

import {
  to = aws_elasticache_parameter_group.queue-valkey8
  id = "queue-valkey8"
}

# default.valkey8: left out by default, would import as "default.valkey8": a default parameter group, which ElastiCache provides for a family and which can't be changed

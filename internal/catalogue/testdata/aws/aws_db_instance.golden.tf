# aws_db_instance, from data/aws/rds.yaml

import {
  to = aws_db_instance.orders
  id = "orders"
}

# Left out of generated configuration: region, tags_all, password

# Kept in state only, so a plan may set: apply_immediately

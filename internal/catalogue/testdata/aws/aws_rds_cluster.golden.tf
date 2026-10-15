# aws_rds_cluster, from data/aws/rds.yaml

import {
  to = aws_rds_cluster.orders
  id = "orders"
}

# Left out of generated configuration: region, tags_all, master_password

# Kept in state only, so a plan may set: enable_global_write_forwarding, enable_local_write_forwarding

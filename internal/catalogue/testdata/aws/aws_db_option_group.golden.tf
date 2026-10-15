# aws_db_option_group, from data/aws/rds.yaml

import {
  to = aws_db_option_group.app-options
  id = "app-options"
}

# default:postgres-17: left out by default, would import as "default:postgres-17": a default option group, which RDS creates for an engine version and Terraform can't manage

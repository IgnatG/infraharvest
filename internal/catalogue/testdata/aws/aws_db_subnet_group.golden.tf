# aws_db_subnet_group, from data/aws/rds.yaml

import {
  to = aws_db_subnet_group.app
  id = "app"
}

# default: left out by default, would import as "default": the default DB subnet group, which RDS creates for the default VPC and Terraform can't manage

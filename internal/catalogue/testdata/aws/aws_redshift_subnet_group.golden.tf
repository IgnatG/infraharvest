# aws_redshift_subnet_group, from data/aws/unassigned.yaml

import {
  to = aws_redshift_subnet_group.warehouse
  id = "warehouse"
}

# default: left out by default, would import as "default": the default cluster subnet group, which Redshift creates for the default VPC and Terraform can't manage

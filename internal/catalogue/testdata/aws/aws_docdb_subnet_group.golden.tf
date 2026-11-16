# aws_docdb_subnet_group, from data/aws/unassigned.yaml

import {
  to = aws_docdb_subnet_group.docs
  id = "docs"
}

# default: left out by default, would import as "default": the default subnet group, which AWS creates for the default VPC and Terraform can't manage

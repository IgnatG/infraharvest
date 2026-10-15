# aws_db_parameter_group, from data/aws/rds.yaml

import {
  to = aws_db_parameter_group.app-postgres17
  id = "app-postgres17"
}

# default.postgres17: left out by default, would import as "default.postgres17": a default parameter group, which RDS provides for a family and which can't be changed

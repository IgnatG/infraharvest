# aws_redshift_parameter_group, from data/aws/unassigned.yaml

import {
  to = aws_redshift_parameter_group.etl-params
  id = "etl-params"
}

# default.redshift-1.0: left out by default, would import as "default.redshift-1.0": a default parameter group, which Redshift provides for a family and which can't be changed

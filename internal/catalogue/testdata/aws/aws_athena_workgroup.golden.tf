# aws_athena_workgroup, from data/aws/athena.yaml

import {
  to = aws_athena_workgroup.analytics
  id = "analytics"
}

# primary: left out by default, would import as "primary": the primary workgroup, which AWS creates for the account

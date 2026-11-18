# aws_kms_alias, from data/aws/kms.yaml

import {
  to = aws_kms_alias.app
  id = "alias/app"
}

# alias/aws/s3: left out by default, would import as "alias/aws/s3": an alias for a key AWS manages for a service, which no one else can create or change

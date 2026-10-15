# aws_kms_key, from data/aws/kms.yaml

import {
  to = aws_kms_key.app
  id = "1234abcd-12ab-34cd-56ef-1234567890ab"
}

# How configuration refers to a resource the selection leaves out:
data "aws_kms_key" "app" {
  key_id = "1234abcd-12ab-34cd-56ef-1234567890ab"
}

# aws_kms_external_key, from data/aws/kms.yaml

import {
  to = aws_kms_external_key.byok
  id = "1234abcd-12ab-34cd-56ef-1234567890ab"
}

# How configuration refers to a resource the selection leaves out:
data "aws_kms_key" "byok" {
  key_id = "1234abcd-12ab-34cd-56ef-1234567890ab"
}

# Left out of generated configuration: region, tags_all, *.tags_all, policy={"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:{*}:iam::{*}:root"},"Action":"kms:*","Resource":"*"}]}

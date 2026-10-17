# aws_kms_replica_key, from data/aws/kms.yaml

import {
  to = aws_kms_replica_key.app_replica
  id = "mrk-0987dcba09fe87dc65baab0987654321"
}

# How configuration refers to a resource the selection leaves out:
data "aws_kms_key" "app_replica" {
  key_id = "mrk-0987dcba09fe87dc65baab0987654321"
}

# Left out of generated configuration: region, tags_all, *.tags_all, policy={"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:{*}:iam::{*}:root"},"Action":"kms:*","Resource":"*"}]}

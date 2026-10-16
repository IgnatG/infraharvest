# aws_redshift_cluster, from data/aws/redshift.yaml

import {
  to = aws_redshift_cluster.analytics
  id = "analytics"
}

import {
  to = aws_redshift_cluster.ledger
  id = "ledger"
}

# Left out of generated configuration: region, tags_all, *.tags_all, kms_key_id=AWS_OWNED_KMS_KEY

# Generated configuration, without the arguments the catalogue omits:
resource "aws_redshift_cluster" "analytics" {
  cluster_identifier = "analytics"
  cluster_type       = "single-node"
  database_name      = "analytics"
  encrypted          = "true"
  node_type          = "ra3.large"
}

resource "aws_redshift_cluster" "ledger" {
  cluster_identifier = "ledger"
  cluster_type       = "single-node"
  database_name      = "ledger"
  encrypted          = "true"
  kms_key_id         = "arn:aws:kms:eu-west-2:123456789012:key/0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"
  node_type          = "ra3.large"
}

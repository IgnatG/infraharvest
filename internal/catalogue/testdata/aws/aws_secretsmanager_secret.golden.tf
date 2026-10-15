# aws_secretsmanager_secret, from data/aws/secretsmanager.yaml

import {
  to = aws_secretsmanager_secret.app
  id = "arn:aws:secretsmanager:us-east-1:123456789012:secret:app-AbCdEf"
}

# Kept in state only, so a plan may set: force_overwrite_replica_secret, recovery_window_in_days

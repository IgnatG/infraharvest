# aws_backup_vault, from data/aws/backup.yaml

import {
  to = aws_backup_vault.app_backups
  id = "app-backups"
}

# Default: left out by default, would import as "Default": a backup vault AWS creates for the account
# aws/efs/automatic-backup-vault: left out by default, would import as "aws/efs/automatic-backup-vault": a backup vault AWS creates for the account

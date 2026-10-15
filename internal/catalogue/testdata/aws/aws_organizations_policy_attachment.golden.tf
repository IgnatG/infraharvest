# aws_organizations_policy_attachment, from data/aws/organization.yaml

import {
  to = aws_organizations_policy_attachment.pa-ou-1_deny-root
  id = "ou-1:p-deny"
}

# r-1:p-FullAWSAccess: left out by default, would import as "r-1:p-FullAWSAccess": the attachment of an AWS managed policy, such as FullAWSAccess, which AWS makes to every root, organizational unit and account

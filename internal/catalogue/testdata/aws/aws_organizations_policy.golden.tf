# aws_organizations_policy, from data/aws/organization.yaml

import {
  to = aws_organizations_policy.deny-root
  id = "p-deny"
}

# p-FullAWSAccess: left out by default, would import as "p-FullAWSAccess": an AWS managed policy, such as FullAWSAccess, which every organization has and AWS maintains

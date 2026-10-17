# aws_cloudformation_stack, from data/aws/cloudformation.yaml

import {
  to = aws_cloudformation_stack.network
  id = "arn:aws:cloudformation:eu-west-1:123456789012:stack/network/1a2b3c4d-0000-4000-8000-000000000001"
}

# arn:aws:cloudformation:eu-west-1:123456789012:stack/awseb-e-abcd1234ef-stack/1a2b3c4d-0000-4000-8000-000000000002: left out by default, would import as "arn:aws:cloudformation:eu-west-1:123456789012:stack/awseb-e-abcd1234ef-stack/1a2b3c4d-0000-4000-8000-000000000002": a stack Elastic Beanstalk creates and manages for an environment
# arn:aws:cloudformation:eu-west-1:123456789012:stack/StackSet-baseline-1a2b3c4d/1a2b3c4d-0000-4000-8000-000000000003: left out by default, would import as "arn:aws:cloudformation:eu-west-1:123456789012:stack/StackSet-baseline-1a2b3c4d/1a2b3c4d-0000-4000-8000-000000000003": a stack a stack set deploys and manages
# arn:aws:cloudformation:eu-west-1:123456789012:stack/SC-123456789012-pp-abcdefghijklm/1a2b3c4d-0000-4000-8000-000000000004: left out by default, would import as "arn:aws:cloudformation:eu-west-1:123456789012:stack/SC-123456789012-pp-abcdefghijklm/1a2b3c4d-0000-4000-8000-000000000004": a stack Service Catalog creates and manages for a provisioned product

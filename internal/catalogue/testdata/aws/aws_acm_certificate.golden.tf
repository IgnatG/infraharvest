# aws_acm_certificate, from data/aws/acm.yaml

import {
  to = aws_acm_certificate.api_example_com
  id = "arn:aws:acm:eu-west-2:123456789012:certificate/1a2b3c4d-0000-4000-8000-000000000004"
}

import {
  to = aws_acm_certificate.www_example_com
  id = "arn:aws:acm:eu-west-2:123456789012:certificate/1a2b3c4d-0000-4000-8000-000000000001"
}

# arn:aws:acm:eu-west-2:123456789012:certificate/1a2b3c4d-0000-4000-8000-000000000002: left out by default, would import as "arn:aws:acm:eu-west-2:123456789012:certificate/1a2b3c4d-0000-4000-8000-000000000002": an expired certificate
# arn:aws:acm:eu-west-2:123456789012:certificate/1a2b3c4d-0000-4000-8000-000000000003: left out by default, would import as "arn:aws:acm:eu-west-2:123456789012:certificate/1a2b3c4d-0000-4000-8000-000000000003": a certificate whose validation timed out, which ACM can't issue

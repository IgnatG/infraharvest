# aws_ssm_parameter, from data/aws/ssm.yaml

import {
  to = aws_ssm_parameter.app_feature_flags
  id = "/app/feature-flags"
}

# Left out of generated configuration: region, tags_all, arn

# Left out where the plan stays the same, as the default: data_type=text, key_id=alias/aws/ssm, tier=Standard

# Generated configuration, without the arguments the catalogue omits:
resource "aws_ssm_parameter" "app_feature_flags" {
  data_type      = "text"
  insecure_value = "beta=on"
  name           = "/app/feature-flags"
  tier           = "Standard"
  type           = "String"
}

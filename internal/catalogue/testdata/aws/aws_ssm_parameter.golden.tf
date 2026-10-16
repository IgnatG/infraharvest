# aws_ssm_parameter, from data/aws/ssm.yaml

import {
  to = aws_ssm_parameter.app_feature_flags
  id = "/app/feature-flags"
}

# Left out of generated configuration: region, tags_all, arn, data_type=text, key_id=alias/aws/ssm, tier=Standard

# Generated configuration, without the arguments the catalogue omits:
resource "aws_ssm_parameter" "app_feature_flags" {
  insecure_value = "beta=on"
  name           = "/app/feature-flags"
  type           = "String"
}

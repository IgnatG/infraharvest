resource "aws_ssm_parameter" "app_feature_flags" {
  arn            = "arn:aws:ssm:eu-west-2:123456789012:parameter/app/feature-flags"
  data_type      = "text"
  insecure_value = "beta=on"
  name           = "/app/feature-flags"
  region         = "eu-west-2"
  tier           = "Standard"
  type           = "String"
}

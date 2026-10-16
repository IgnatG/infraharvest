resource "aws_route53_zone" "internal" {
  comment                     = "Managed by Terraform"
  enable_accelerated_recovery = false
  force_destroy               = false
  name                        = "example.internal"
  vpc {
    vpc_id     = "vpc-0a1b2c3d4e5f60718"
    vpc_region = "eu-west-2"
  }
}

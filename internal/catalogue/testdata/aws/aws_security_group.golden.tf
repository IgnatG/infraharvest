# aws_security_group, from data/aws/sg.yaml

import {
  to = aws_security_group.main_default
  id = "sg-0c3d4e5f607182930"
}

import {
  to = aws_security_group.web
  id = "sg-0a1b2c3d4e5f60718"
}

# sg-0b2c3d4e5f6071829: left out by default, would import as "sg-0b2c3d4e5f6071829": the default security group AWS creates with every VPC, unchanged, with only its default rules; select it and its rules to import them

# How configuration refers to a resource the selection leaves out:
data "aws_security_group" "web" {
  id = "sg-0a1b2c3d4e5f60718"
}

data "aws_security_group" "default" {
  id = "sg-0b2c3d4e5f6071829"
}

data "aws_security_group" "main_default" {
  id = "sg-0c3d4e5f607182930"
}

# Left out of generated configuration: region, tags_all, *.tags_all, ingress, egress

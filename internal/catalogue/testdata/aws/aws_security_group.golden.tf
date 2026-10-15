# aws_security_group, from data/aws/sg.yaml

import {
  to = aws_security_group.web
  id = "sg-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_security_group" "web" {
  id = "sg-0a1b2c3d4e5f60718"
}

# Left out of generated configuration: region, tags_all, ingress, egress

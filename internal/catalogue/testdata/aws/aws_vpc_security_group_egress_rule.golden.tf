# aws_vpc_security_group_egress_rule, from data/aws/sg.yaml

import {
  to = aws_vpc_security_group_egress_rule.web_egress_all
  id = "sgr-0f1e2d3c4b5a69788"
}

# sgr-0c3d4e5f607182930: left out by default, would import as "sgr-0c3d4e5f607182930": a rule AWS gives the default security group of every VPC, which is unchanged; select it with its group to import it

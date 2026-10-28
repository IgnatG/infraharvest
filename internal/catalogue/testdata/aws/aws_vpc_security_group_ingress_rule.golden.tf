# aws_vpc_security_group_ingress_rule, from data/aws/sg.yaml

import {
  to = aws_vpc_security_group_ingress_rule.web_ingress_tcp_443
  id = "sgr-0a1b2c3d4e5f60718"
}

# sgr-0b2c3d4e5f6071829: left out by default, would import as "sgr-0b2c3d4e5f6071829": a rule AWS gives the default security group of every VPC, which is unchanged; select it with its group to import it

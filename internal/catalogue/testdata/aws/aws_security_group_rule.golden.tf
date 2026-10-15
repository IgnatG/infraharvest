# aws_security_group_rule, from data/aws/sg.yaml

import {
  to = aws_security_group_rule.web_egress
  id = "sg-0a1_egress_all_0_65536_0.0.0.0/0"
}

import {
  to = aws_security_group_rule.web_https
  id = "sg-0a1_ingress_tcp_443_443_10.0.0.0/16"
}

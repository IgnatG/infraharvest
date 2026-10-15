# aws_route_table_association, from data/aws/route_table.yaml

import {
  to = aws_route_table_association.edge
  id = "igw-0a1/rtb-0b2"
}

import {
  to = aws_route_table_association.private_a
  id = "subnet-0a1/rtb-0a1"
}

# rtbassoc-03: not importable: missing a field of every import ID template: {subnet_id}/{route_table_id}, {gateway_id}/{route_table_id}

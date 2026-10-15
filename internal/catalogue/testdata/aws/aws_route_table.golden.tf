# aws_route_table, from data/aws/route_table.yaml

import {
  to = aws_route_table.private
  id = "rtb-0a1b2c3d4e5f60718"
}

# How configuration refers to a resource the selection leaves out:
data "aws_route_table" "private" {
  route_table_id = "rtb-0a1b2c3d4e5f60718"
}

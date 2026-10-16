# aws_route_table, from data/aws/route_table.yaml

import {
  to = aws_route_table.private
  id = "rtb-0a1b2c3d4e5f60718"
}

import {
  to = aws_route_table.shared_main
  id = "rtb-0123456789abcdef0"
}

# rtb-0f1e2d3c4b5a69788: left out by default, would import as "rtb-0f1e2d3c4b5a69788": the main route table AWS creates with every VPC, unchanged, with only the VPC's local routes; the VPC records it as its main_route_table_id

# How configuration refers to a resource the selection leaves out:
data "aws_route_table" "private" {
  route_table_id = "rtb-0a1b2c3d4e5f60718"
}

data "aws_route_table" "main" {
  route_table_id = "rtb-0f1e2d3c4b5a69788"
}

data "aws_route_table" "shared_main" {
  route_table_id = "rtb-0123456789abcdef0"
}

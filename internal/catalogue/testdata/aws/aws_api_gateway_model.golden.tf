# aws_api_gateway_model, from data/aws/api_gateway.yaml

import {
  to = aws_api_gateway_model.orders_Order
  id = "a1b2c3d4e5/Order"
}

# a1b2c3d4e5/Empty: left out by default, would import as "a1b2c3d4e5/Empty": a model AWS creates with every REST API
# a1b2c3d4e5/Error: left out by default, would import as "a1b2c3d4e5/Error": a model AWS creates with every REST API

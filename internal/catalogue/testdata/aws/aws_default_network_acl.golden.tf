# aws_default_network_acl, from data/aws/nacl.yaml

import {
  to = aws_default_network_acl.main_default
  id = "acl-0f1e2d3c4b5a69788"
}

# acl-0a1b2c3d4e5f60718: left out by default, would import as "acl-0a1b2c3d4e5f60718": the default network ACL AWS creates with every VPC, unchanged, with only its allow-all rules; select it to import it

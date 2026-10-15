# aws_instance, from data/aws/ec2_instance.yaml

import {
  to = aws_instance.app
  id = "i-0a1b2c3d4e5f60718"
}

# Left out of generated configuration: region, tags_all, primary_network_interface

# Generated configuration, without the arguments the catalogue omits:
resource "aws_instance" "app" {
  ami                         = "ami-0123456789abcdef0"
  associate_public_ip_address = false
  instance_type               = "t4g.nano"
  private_ip                  = "10.0.11.10"
  subnet_id                   = "subnet-0a1b2c3d4e5f60718"
  vpc_security_group_ids      = ["sg-0a1b2c3d4e5f60718"]
}

resource "aws_instance" "app" {
  ami                         = "ami-0123456789abcdef0"
  associate_public_ip_address = false
  instance_type               = "t4g.nano"
  private_ip                  = "10.0.11.10"
  subnet_id                   = "subnet-0a1b2c3d4e5f60718"
  vpc_security_group_ids      = ["sg-0a1b2c3d4e5f60718"]
  primary_network_interface {
    network_interface_id = "eni-0a1b2c3d4e5f60718"
  }
}

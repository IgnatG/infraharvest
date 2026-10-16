resource "aws_eip" "nat" {
  domain               = "vpc"
  network_border_group = "eu-west-2"
  public_ipv4_pool     = "amazon"
  region               = "eu-west-2"
  tags = {
    Name = "nat"
  }
}

resource "aws_eip" "edge" {
  domain               = "vpc"
  network_border_group = "us-west-2-lax-1"
  public_ipv4_pool     = "ipv4pool-ec2-0a1b2c3d4e5f60718"
  region               = "us-west-2"
}

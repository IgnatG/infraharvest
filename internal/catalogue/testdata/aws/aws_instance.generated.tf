resource "aws_instance" "app" {
  ami                                  = "ami-0123456789abcdef0"
  associate_public_ip_address          = false
  availability_zone                    = "eu-west-2a"
  disable_api_stop                     = false
  disable_api_termination              = false
  ebs_optimized                        = false
  enable_primary_ipv6                  = false
  instance_initiated_shutdown_behavior = "stop"
  instance_type                        = "t4g.nano"
  ipv6_address_count                   = 0
  monitoring                           = false
  placement_partition_number           = 0
  private_ip                           = "10.0.11.10"
  region                               = "eu-west-2"
  subnet_id                            = "subnet-0a1b2c3d4e5f60718"
  tenancy                              = "default"
  vpc_security_group_ids               = ["sg-0a1b2c3d4e5f60718"]
  capacity_reservation_specification {
    capacity_reservation_preference = "open"
  }
  cpu_options {
    core_count       = 2
    threads_per_core = 1
  }
  credit_specification {
    cpu_credits = "unlimited"
  }
  enclave_options {
    enabled = false
  }
  maintenance_options {
    auto_recovery = "default"
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_protocol_ipv6          = "disabled"
    http_put_response_hop_limit = 2
    http_tokens                 = "required"
    instance_metadata_tags      = "disabled"
  }
  primary_network_interface {
    network_interface_id = "eni-0a1b2c3d4e5f60718"
  }
  private_dns_name_options {
    enable_resource_name_dns_a_record    = false
    enable_resource_name_dns_aaaa_record = false
    hostname_type                        = "ip-name"
  }
  root_block_device {
    delete_on_termination = true
    encrypted             = true
    iops                  = 3000
    throughput            = 125
    tags_all = {
      Name = "app"
    }
    volume_size           = 8
    volume_type           = "gp3"
  }
}

resource "aws_instance" "db" {
  ami                                  = "ami-0123456789abcdef0"
  availability_zone                    = "eu-west-2b"
  disable_api_termination              = true
  ebs_optimized                        = true
  instance_initiated_shutdown_behavior = "stop"
  instance_type                        = "m5.xlarge"
  monitoring                           = true
  region                               = "eu-west-2"
  subnet_id                            = "subnet-0f1e2d3c4b5a69788"
  tenancy                              = "default"
  vpc_security_group_ids               = ["sg-0a1b2c3d4e5f60718"]
  cpu_options {
    core_count       = 1
    threads_per_core = 2
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_protocol_ipv6          = "disabled"
    http_put_response_hop_limit = 1
    http_tokens                 = "optional"
    instance_metadata_tags      = "enabled"
  }
  root_block_device {
    delete_on_termination = true
    encrypted             = false
    iops                  = 6000
    throughput            = 250
    volume_size           = 100
    volume_type           = "gp3"
  }
}

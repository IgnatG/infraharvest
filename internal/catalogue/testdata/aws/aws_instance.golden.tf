# aws_instance, from data/aws/ec2_instance.yaml

import {
  to = aws_instance.app
  id = "i-0a1b2c3d4e5f60718"
}

import {
  to = aws_instance.db
  id = "i-0f1e2d3c4b5a69788"
}

# Left out of generated configuration: region, tags_all, *.tags_all, primary_network_interface, availability_zone when subnet_id, root_block_device.iops=3000 when root_block_device.volume_type=gp3, root_block_device.throughput=125 when root_block_device.volume_type=gp3, cpu_options when @cpu_options=default, credit_specification when instance_type=t2.{*} and credit_specification.cpu_credits=standard, credit_specification when instance_type=t3.{*} and credit_specification.cpu_credits=unlimited, credit_specification when instance_type=t3a.{*} and credit_specification.cpu_credits=unlimited, credit_specification when instance_type=t4g.{*} and credit_specification.cpu_credits=unlimited

# i-0a1b2c3d4e5f60718: also left out, from what its lister recorded: cpu_options

# Left out where the plan stays the same, as the default: capacity_reservation_specification.capacity_reservation_preference=open, disable_api_stop=false, disable_api_termination=false, ebs_optimized=false, enable_primary_ipv6=false, enclave_options.enabled=false, instance_initiated_shutdown_behavior=stop, ipv6_address_count=0, maintenance_options.auto_recovery=default, metadata_options.instance_metadata_tags=disabled, monitoring=false, placement_partition_number=0, private_dns_name_options.enable_resource_name_dns_a_record=false, private_dns_name_options.enable_resource_name_dns_aaaa_record=false, private_dns_name_options.hostname_type=ip-name, tenancy=default

# Generated configuration, without the arguments the catalogue omits:
resource "aws_instance" "app" {
  ami                                  = "ami-0123456789abcdef0"
  associate_public_ip_address          = false
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
  subnet_id                            = "subnet-0a1b2c3d4e5f60718"
  tenancy                              = "default"
  vpc_security_group_ids               = ["sg-0a1b2c3d4e5f60718"]
  capacity_reservation_specification {
    capacity_reservation_preference = "open"
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
  private_dns_name_options {
    enable_resource_name_dns_a_record    = false
    enable_resource_name_dns_aaaa_record = false
    hostname_type                        = "ip-name"
  }
  root_block_device {
    delete_on_termination = true
    encrypted             = true
    volume_size           = 8
    volume_type           = "gp3"
  }
}

resource "aws_instance" "db" {
  ami                                  = "ami-0123456789abcdef0"
  disable_api_termination              = true
  ebs_optimized                        = true
  instance_initiated_shutdown_behavior = "stop"
  instance_type                        = "m5.xlarge"
  monitoring                           = true
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

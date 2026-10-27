# ---- data.tf ----

data "aws_region" "current" {}

data "aws_ssm_parameter" "image_id" {
  name = var.image_id
}

# ---- locals.tf ----

locals {
  sizes = {
    small = {
      Type = "t3.small"
    }
    large = {
      Type = "m6i.large"
    }
  }
}

# ---- main.tf ----

# warning: the template uses the AWS::LanguageExtensions transform: Fn::Length, Fn::ToJsonString and Fn::FindInMap defaults are converted, but Fn::ForEach and functions in places only the transform allows aren't

# Profile (AWS::IAM::InstanceProfile)
# Check these argument names, derived from the property names: roles (Roles).
resource "aws_iam_instance_profile" "profile" {
  roles = ["web"]
}

# Server (AWS::EC2::Instance)
# DeletionPolicy Snapshot: CloudFormation snapshots the resource before it deletes it. Set the resource's own final snapshot arguments, such as skip_final_snapshot and final_snapshot_identifier on aws_db_instance
resource "aws_instance" "server" {
  ami                  = data.aws_ssm_parameter.image_id.insecure_value
  instance_type        = try(local.sizes["medium"]["Type"], "t3.medium")
  subnet_id            = element(var.subnets, 0)
  iam_instance_profile = aws_iam_instance_profile.profile.name
  user_data_base64 = base64encode(<<EOT
#!/bin/bash
echo "port count: ${length([80, 443])}" > /etc/motd
echo "region ${data.aws_region.current.region}, stack ${var.stack_name}"
export DB_PASSWORD='${var.db_password}'
echo "literal $${HOME} and 100%%{done}"
EOT
  )
  tags = {
    Ports = jsonencode([80, 443])
  }
}

# Disk (AWS::EC2::Volume)
resource "aws_ebs_volume" "disk" {
  availability_zone = aws_instance.server.availability_zone
  size              = 20
  encrypted         = true
}

# ---- outputs.tf ----

output "private_ip" {
  value = aws_instance.server.private_ip
}

output "disk" {
  value = aws_ebs_volume.disk.id
}

# ---- variables.tf ----

variable "image_id" {
  type        = string
  description = "The name of the SSM parameter to read the value of: the CloudFormation type was AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>."
  default     = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
}

variable "subnets" {
  type        = list(string)
  description = "CloudFormation type List<AWS::EC2::Subnet::Id>."
}

variable "ports" {
  type    = list(number)
  default = [80, 443]
}

variable "db_password" {
  type      = string
  sensitive = true
  validation {
    condition     = length(var.db_password) >= 12
    error_message = "The value of DbPassword must be at least 12 characters long."
  }
}

variable "stack_name" {
  type        = string
  description = "Stands in for AWS::StackName: Terraform has no stacks, so set it to the name the template's resources were named after."
}

# ---- diagnostics ----
# warning: Transform: the template uses the AWS::LanguageExtensions transform: Fn::Length, Fn::ToJsonString and Fn::FindInMap defaults are converted, but Fn::ForEach and functions in places only the transform allows aren't
# info: Resources.Profile.Properties: argument names derived from the property names, to check against the provider's schema: roles (Roles)
# info: Resources.Server.DeletionPolicy: DeletionPolicy Snapshot: CloudFormation snapshots the resource before it deletes it. Set the resource's own final snapshot arguments, such as skip_final_snapshot and final_snapshot_identifier on aws_db_instance
# info: Resources.Server.Properties.UserData.Fn::Base64.Fn::Sub: AWS::StackName is the variable stack_name: Terraform has no stacks

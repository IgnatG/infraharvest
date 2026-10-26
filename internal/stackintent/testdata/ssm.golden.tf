# ---- cloudformation_ssm.tf ----

# From the template of the CloudFormation stack ssm: the parameters it
# sets arguments from, as variables whose defaults are the stack's values,
# and its outputs. The resources keep their live configuration.
#
# Not carried over:
#   - aws_sns_topic.events.name differs from the value of the parameter TopicName, which sets it in the template: the stack has drifted, and aws_sns_topic.events keeps the live value
#   - Group.DBSubnetGroupName (Ref GroupName): the value CloudFormation resolved the SSM parameter to isn't known

variable "queue_name" {
  type        = string
  description = "Name of the jobs queue. (The name of the SSM parameter to read the value of: the CloudFormation type was AWS::SSM::Parameter::Value<String>)"
  default     = "/app/queue-name"
}

variable "subnets" {
  type        = string
  description = "The Subnets parameter of the CloudFormation stack ssm. (The name of the SSM parameter to read the value of: the CloudFormation type was AWS::SSM::Parameter::Value<List<AWS::EC2::Subnet::Id>>)"
  default     = "/app/subnets"
}

data "aws_ssm_parameter" "queue_name_2" {
  name = var.queue_name
}

data "aws_ssm_parameter" "subnets" {
  name = var.subnets
}

output "queue_label" {
  description = "The QueueLabel output of the CloudFormation stack ssm."
  value       = "${data.aws_ssm_parameter.queue_name_2.insecure_value}:${aws_sqs_queue.jobs.arn}"
}

# ---- data.tf ----

data "aws_ssm_parameter" "queue_name" {
  name = "/other/queue-name"
}

# ---- main.tf ----

resource "aws_sqs_queue" "jobs" {
  name = data.aws_ssm_parameter.queue_name_2.insecure_value
}

resource "aws_sns_topic" "events" {
  name = "app-events-old"
}

resource "aws_db_subnet_group" "app" {
  name        = "app-group"
  description = "App subnets"
  subnet_ids  = split(",", data.aws_ssm_parameter.subnets.insecure_value)
}

# ---- warnings ----
# CloudFormation stack ssm: aws_sns_topic.events.name differs from the value of the parameter TopicName, which sets it in the template: the stack has drifted, and aws_sns_topic.events keeps the live value

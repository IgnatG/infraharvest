# ---- cloudformation_app.tf ----

# From the template of the CloudFormation stack app: the parameters it
# sets arguments from, as variables whose defaults are the stack's values,
# and its outputs. The resources keep their live configuration.
#
# Its conditions, which decided what the stack has; the root has what was
# deployed, so they decide nothing here:
#   IsProd: {"Fn::Equals":[{"Ref":"Env"},"prod"]}
#
# Not carried over:
#   - Bucket.Tags is computed from the parameters Env, so aws_s3_bucket.app_data keeps its value
#   - Logs.LogGroupName is computed from the parameters Env, so aws_cloudwatch_log_group.app keeps its value
#   - aws_sns_topic.events.name differs from the value of the parameter Env, which sets it in the template: the stack has drifted, and aws_sns_topic.events keeps the live value
#   - outputs not written in this root: QueueUrl (Queue (AWS::SQS::Queue) has no Terraform address here; it is null instead); StackLabel (it refers to no resource); ProdOnly (it has the condition IsProd); WithSecret (Ref to the parameter Secret, which has no value here; it is null instead)

variable "bucket_name_2" {
  type        = string
  description = "Name of the data bucket."
  default     = "app-data-123"
}

variable "retention" {
  type        = number
  description = "The Retention parameter of the CloudFormation stack app."
  default     = 30
}

variable "env" {
  type        = string
  description = "The Env parameter of the CloudFormation stack app."
  default     = "prod"

  validation {
    condition     = contains(["dev", "prod"], var.env)
    error_message = "The value must be one the template allows for Env."
  }
}

# CloudFormation exported this output as {"Fn::Sub":"${AWS::StackName}-BucketArn"}: Terraform has no exports, so another configuration reads it from this root's state.
output "bucket_arn" {
  description = "The data bucket."
  value       = aws_s3_bucket.app_data.arn
}

output "log_group" {
  description = "The LogGroup output of the CloudFormation stack app."
  value       = aws_cloudwatch_log_group.app.name
}

output "labelled" {
  description = "The Labelled output of the CloudFormation stack app."
  value       = "app/${var.env}/${aws_s3_bucket.app_data.id}"
}

# ---- logs.tf ----

resource "aws_cloudwatch_log_group" "app" {
  name              = "/app/prod"
  retention_in_days = var.retention
}

# ---- s3.tf ----

resource "aws_s3_bucket" "app_data" {
  bucket = var.bucket_name_2
  tags = {
    env = "prod"
  }
}

# ---- sns.tf ----

resource "aws_sns_topic" "events" {
  name = "prod-events"
}

# ---- variables.tf ----

variable "bucket_name" {
  type        = string
  description = "Taken already, so the parameter's variable gets another name."
  default     = null
}

# ---- warnings ----
# CloudFormation stack app: aws_sns_topic.events.name differs from the value of the parameter Env, which sets it in the template: the stack has drifted, and aws_sns_topic.events keeps the live value
# CloudFormation stack app: outputs not written in this root: QueueUrl (Queue (AWS::SQS::Queue) has no Terraform address here; it is null instead); StackLabel (it refers to no resource); ProdOnly (it has the condition IsProd); WithSecret (Ref to the parameter Secret, which has no value here; it is null instead)

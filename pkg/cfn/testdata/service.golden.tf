# ---- data.tf ----

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

# ---- locals.tf ----

locals {
  tracing = var.enable_tracing == "true"
  large   = (var.memory_size == 512) || local.tracing
}

# ---- main.tf ----

# Bucket (AWS::S3::Bucket)
# DeletionPolicy Retain: CloudFormation keeps the resource when the stack deletes it, and Terraform destroys what leaves the configuration. To keep it, remove it with a removed block whose lifecycle sets destroy = false
# UpdateReplacePolicy Retain: CloudFormation keeps the old resource when an update replaces it, and Terraform destroys it
resource "aws_s3_bucket" "bucket" {
  bucket = "${var.function_name}-${data.aws_caller_identity.current.account_id}-output"
  # error: VersioningConfiguration is left out: the AWS provider manages it with a separate aws_s3_bucket_versioning resource
  # It was: {"Status":"Enabled"}
  tags = { for tag in [for v in [
    {
      Key   = "app"
      Value = var.function_name
    },
    local.tracing ? {
      Key   = "tracing"
      Value = "on"
    } : null,
  ] : v if v != null] : tag.Key => tag.Value if tag != null }
}

# Role (AWS::IAM::Role)
resource "aws_iam_role" "role" {
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Service = "lambda.amazonaws.com"
        }
        Action = "sts:AssumeRole"
        Condition = {
          StringEquals = {
            "aws:SourceAccount" = data.aws_caller_identity.current.account_id
          }
        }
      },
    ]
  })
  managed_policy_arns = [
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole",
  ]
  inline_policy {
    name = "write-output"
    policy = jsonencode({
      Version = "2012-10-17"
      Statement = [
        {
          Effect   = "Allow"
          Action   = ["s3:PutObject"]
          Resource = "${aws_s3_bucket.bucket.arn}/*"
        },
      ]
    })
  }
}

# Logs (AWS::Logs::LogGroup)
resource "aws_cloudwatch_log_group" "logs" {
  name              = "/aws/lambda/${var.function_name}"
  retention_in_days = local.large ? 30 : 7
}

# Function (AWS::Lambda::Function)
# Check these argument names, derived from the property names: tracing_config (TracingConfig), mode (TracingConfig.Mode).
resource "aws_lambda_function" "function" {
  function_name = var.function_name
  runtime       = "python3.13"
  handler       = "app.handler"
  role          = aws_iam_role.role.arn
  memory_size   = var.memory_size
  # error: Code is left out: the AWS provider takes the code as the filename, s3_bucket and s3_key, or image_uri arguments
  # It was: {"S3Bucket":"artifacts","S3Key":"app.zip"}
  environment {
    # warning: Fn::ImportValue reads the export ${AWS::Region}-SharedTable of another stack, and Terraform has no exports: it is the variable import_aws_region_shared_table, to set from that stack's output or a terraform_remote_state data source
    variables = {
      BUCKET       = aws_s3_bucket.bucket.id
      API_KEY      = var.api_key
      SHARED_TABLE = var.import_aws_region_shared_table
    }
  }
  tracing_config = local.tracing ? {
    mode = "Active"
  } : null
  depends_on = [aws_cloudwatch_log_group.logs]
}

# Schedule (AWS::Events::Rule)
resource "aws_cloudwatch_event_rule" "schedule" {
  schedule_expression = "rate(1 hour)"
  event_pattern = jsonencode({
    source = ["aws.s3"]
  })
  # error: Targets is left out: the AWS provider manages each target with an aws_cloudwatch_event_target resource
  # It was: [{"Arn":{"Fn::GetAtt":["Function","Arn"]},"Id":"function"}]
}

# ---- outputs.tf ----

# CloudFormation exports this value as service-function-arn. Terraform has no exports: another configuration reads the output from this one's state, with a terraform_remote_state data source
output "function_arn" {
  value = aws_lambda_function.function.arn
}

output "api_key" {
  value     = var.api_key
  sensitive = true
}

output "tracing_note" {
  value = local.tracing ? (local.large ? "large and traced" : "traced") : null
}

# ---- variables.tf ----

variable "function_name" {
  type = string
  validation {
    condition     = length(var.function_name) >= 1
    error_message = "The value of FunctionName must be at least 1 characters long."
  }
  validation {
    condition     = length(var.function_name) <= 64
    error_message = "The value of FunctionName must be at most 64 characters long."
  }
}

variable "memory_size" {
  type    = number
  default = 256
  validation {
    condition     = contains([128, 256, 512], var.memory_size)
    error_message = "The value of MemorySize must be one of 128, 256, 512."
  }
}

# warning: the default of a NoEcho parameter isn't written, so that the secret stays out of the configuration: set the variable from a secret store
variable "api_key" {
  type      = string
  sensitive = true
}

variable "enable_tracing" {
  type    = string
  default = "false"
  validation {
    condition     = contains(["true", "false"], var.enable_tracing)
    error_message = "The value of EnableTracing must be one of true, false."
  }
}

variable "import_aws_region_shared_table" {
  type        = string
  description = "Stands in for Fn::ImportValue of the export $${AWS::Region}-SharedTable: Terraform has no exports, so set it from the exporting stack's output."
}

# ---- diagnostics ----
# warning: Parameters.ApiKey.Default: the default of a NoEcho parameter isn't written, so that the secret stays out of the configuration: set the variable from a secret store
# info: Resources.Bucket.DeletionPolicy: DeletionPolicy Retain: CloudFormation keeps the resource when the stack deletes it, and Terraform destroys what leaves the configuration. To keep it, remove it with a removed block whose lifecycle sets destroy = false
# info: Resources.Bucket.UpdateReplacePolicy: UpdateReplacePolicy Retain: CloudFormation keeps the old resource when an update replaces it, and Terraform destroys it
# error: Resources.Bucket.Properties.VersioningConfiguration: VersioningConfiguration is left out: the AWS provider manages it with a separate aws_s3_bucket_versioning resource
# error: Resources.Function.Properties.Code: Code is left out: the AWS provider takes the code as the filename, s3_bucket and s3_key, or image_uri arguments
# warning: Resources.Function.Properties.Environment.Variables.SHARED_TABLE.Fn::ImportValue: Fn::ImportValue reads the export ${AWS::Region}-SharedTable of another stack, and Terraform has no exports: it is the variable import_aws_region_shared_table, to set from that stack's output or a terraform_remote_state data source
# info: Resources.Function.Properties: argument names derived from the property names, to check against the provider's schema: tracing_config (TracingConfig), mode (TracingConfig.Mode)
# error: Resources.Schedule.Properties.Targets: Targets is left out: the AWS provider manages each target with an aws_cloudwatch_event_target resource
# info: Outputs.FunctionArn.Export: CloudFormation exports this value as service-function-arn. Terraform has no exports: another configuration reads the output from this one's state, with a terraform_remote_state data source
# info: Outputs.ApiKey: the output is sensitive: its value comes from a NoEcho parameter

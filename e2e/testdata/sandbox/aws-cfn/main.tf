# Resources the CloudFormation sandbox test (TestSandboxAWSCloudFormation,
# in sandbox_aws_test.go) creates in a real AWS account before importing
# them with --cfn-stack: two CloudFormation stacks, and the bucket the
# nested stack's template is read from. The stack app holds a bucket with
# the default DeletionPolicy, a queue and its policy, a role, a Lambda
# function that is the provider of a custom resource, and a nested stack
# with an SNS topic. The stack old holds a bucket it retains; the test
# deletes it before importing, so that only its stack ID finds it. Every
# name starts with ih-sbx-, and the stacks' tags, the provider's default
# tags, reach every resource of theirs that takes tags, so the sweep finds
# what a run leaves; the test deletes what the stacks retain itself.
# The provider is configured through the environment the test sets.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same account, such as one per engine."
  type        = string
  default     = "terraform"
}

variable "run_id" {
  description = "The CI run that applies the estate, if any. Every resource is tagged with it, so the run's last sweep finds what the run left, and nothing else."
  type        = string
  default     = ""
}

variable "permissions_boundary_arn" {
  description = "The permissions boundary of every IAM role the estate creates, which the fixture role may create no role without (see aws-bootstrap). Empty creates them without, as on an emulator."
  type        = string
  default     = ""
}

locals {
  # The tags every resource carries, so a sweep finds what a run left.
  sandbox_tags = { for k, v in { infraharvest-sandbox = var.suffix, infraharvest-sandbox-run = var.run_id } : k => v if v != "" }

  permissions_boundary = var.permissions_boundary_arn == "" ? null : var.permissions_boundary_arn
}

provider "aws" {
  default_tags {
    tags = local.sandbox_tags
  }
}

data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

locals {
  name    = "ih-sbx-${var.suffix}-cfn"
  account = data.aws_caller_identity.current.account_id
}

# Where CloudFormation reads the nested stack's template from. The test
# writes the copy with Retain added over it, as a user would.
resource "aws_s3_bucket" "templates" {
  bucket        = "${local.name}-${local.account}-templates"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "templates" {
  bucket                  = aws_s3_bucket.templates.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_object" "network" {
  bucket = aws_s3_bucket.templates.id
  key    = "network.yaml"
  content = yamlencode({
    Resources = {
      Alerts = {
        Type       = "AWS::SNS::Topic"
        Properties = { TopicName = "${local.name}-alerts" }
      }
    }
  })
}

resource "aws_cloudformation_stack" "app" {
  name         = local.name
  capabilities = ["CAPABILITY_NAMED_IAM"]
  depends_on   = [aws_s3_bucket_public_access_block.templates]
  template_body = yamlencode({
    Resources = {
      # No DeletionPolicy: deleting the stack would delete it.
      Assets = {
        Type       = "AWS::S3::Bucket"
        Properties = { BucketName = "${local.name}-${local.account}-assets" }
      }
      Jobs = {
        Type       = "AWS::SQS::Queue"
        Properties = { QueueName = "${local.name}-jobs" }
      }
      # Its physical ID isn't the queue's URL, which Terraform imports it by:
      # infraharvest finds it from the queue it names.
      JobsPolicy = {
        Type = "AWS::SQS::QueuePolicy"
        Properties = {
          Queues = [{ Ref = "Jobs" }]
          PolicyDocument = {
            Version = "2012-10-17"
            Statement = [{
              Effect    = "Allow"
              Principal = { AWS = "arn:aws:iam::${local.account}:root" }
              Action    = "sqs:SendMessage"
              Resource  = { "Fn::GetAtt" = ["Jobs", "Arn"] }
            }]
          }
        }
      }
      Role = {
        Type = "AWS::IAM::Role"
        Properties = merge({
          RoleName = "${local.name}-seed"
          AssumeRolePolicyDocument = {
            Version   = "2012-10-17"
            Statement = [{ Effect = "Allow", Principal = { Service = "lambda.amazonaws.com" }, Action = "sts:AssumeRole" }]
          }
          ManagedPolicyArns = ["arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"]
        }, local.permissions_boundary == null ? {} : { PermissionsBoundary = local.permissions_boundary })
      }
      # The provider of the custom resource Seed, which logs each request's
      # type, for the test to tell whether it was called, and answers success.
      SeedFunction = {
        Type = "AWS::Lambda::Function"
        Properties = {
          FunctionName = "${local.name}-seed"
          Runtime      = "python3.13"
          Handler      = "index.handler"
          Role         = { "Fn::GetAtt" = ["Role", "Arn"] }
          Timeout      = 30
          Code = {
            ZipFile = join("\n", [
              "import cfnresponse",
              "def handler(event, context):",
              "    print('seed: ' + event['RequestType'])",
              "    cfnresponse.send(event, context, cfnresponse.SUCCESS, {}, 'seed')",
            ])
          }
        }
      }
      Seed = {
        Type       = "Custom::Seed"
        Properties = { ServiceToken = { "Fn::GetAtt" = ["SeedFunction", "Arn"] } }
      }
      Network = {
        Type       = "AWS::CloudFormation::Stack"
        Properties = { TemplateURL = "https://${aws_s3_bucket.templates.bucket_regional_domain_name}/${aws_s3_object.network.key}" }
      }
    }
  })
}

# Deleted by the test before it imports: the bucket stays, retained.
resource "aws_cloudformation_stack" "old" {
  name = "${local.name}-old"
  template_body = yamlencode({
    Resources = {
      Kept = {
        Type                = "AWS::S3::Bucket"
        DeletionPolicy      = "Retain"
        UpdateReplacePolicy = "Retain"
        Properties          = { BucketName = "${local.name}-${local.account}-kept" }
      }
    }
  })
}

output "templates_bucket" {
  value = aws_s3_bucket.templates.id
}

output "network_key" {
  value = aws_s3_object.network.key
}

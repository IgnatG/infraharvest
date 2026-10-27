# The role's deny on data reads: it covers the data-plane reads that
# ReadOnlyAccess grants, and none of the configuration reads infraharvest
# and the generated roots' plans need. Planned against a mock provider, so
# it needs no AWS account.

variables {
  trusted_principal_arns = ["arn:aws:iam::123456789012:role/platform-ci"]
}

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }

  mock_data "aws_partition" {
    defaults = {
      partition = "aws"
    }
  }
}

run "data_reads_denied" {
  command = plan

  assert {
    condition = length(setsubtract([
      # Objects, by every route ReadOnlyAccess opens to them.
      "s3:GetObject",
      "s3:GetObjectVersionForReplication",
      "s3-object-lambda:GetObject",
      "s3express:CreateSession",
      "s3tables:GetTableData",
      "glacier:GetJobOutput",
      # Items, documents, records and messages.
      "dynamodb:PartiQLSelect",
      "dax:Scan",
      "es:ESHttpGet",
      "es:ESHttpHead",
      "kinesisvideo:GetMedia",
      "iot:GetThingShadow",
      # Logs, traces and execution data.
      "logs:StartLiveTail",
      "logs:GetLogObject",
      "cloudtrail:LookupEvents",
      "xray:BatchGetTraces",
      "states:GetExecutionHistory",
      "rds:DownloadDBLogFilePortion",
    ], jsondecode(aws_iam_role_policy.deny_data_reads.policy).Statement[0].Action)) == 0
    error_message = "A data-plane read ReadOnlyAccess grants isn't denied."
  }

  assert {
    condition = length(setintersection([
      "s3:GetBucketPolicy",
      "s3:ListBucket",
      "dynamodb:DescribeTable",
      "es:DescribeDomain",
      "lambda:GetFunction",
      "ssm:GetParameter",
      "cassandra:Select",
      "logs:DescribeLogGroups",
      "cloudtrail:GetTrail",
      "states:DescribeStateMachine",
      "kinesisvideo:DescribeStream",
      "appconfig:GetHostedConfigurationVersion",
      "cloudfront-keyvaluestore:GetKey",
    ], jsondecode(aws_iam_role_policy.deny_data_reads.policy).Statement[0].Action)) == 0
    error_message = "A configuration read that listing or importing needs is denied."
  }
}

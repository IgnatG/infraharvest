# The drift role denies the read-only role's data reads too, all but the
# state's everywhere. Planned against a mock provider, so it needs no AWS
# account.

variables {
  state_bucket           = "acme-tfstate"
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

run "data_reads_denied_but_the_state" {
  command = plan

  assert {
    condition = length(setsubtract(
      [for action in jsondecode(file("../aws/deny-data-reads.json")).Statement[0].Action : action if !contains(["s3:GetObject", "s3:GetObjectTorrent", "s3:GetObjectVersion"], action)],
      one([for s in data.aws_iam_policy_document.deny_data_reads.statement : s.actions if s.sid == "DenyDataPlaneReads"]),
    )) == 0
    error_message = "A data read the read-only role denies isn't denied everywhere."
  }

  assert {
    condition     = toset(one([for s in data.aws_iam_policy_document.deny_data_reads.statement : s.actions if s.sid == "DenyObjectReadsButTheState"])) == toset(["s3:GetObject", "s3:GetObjectTorrent", "s3:GetObjectVersion"])
    error_message = "The state's object reads aren't denied everywhere but the state."
  }
}

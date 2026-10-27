# The role's trust in IAM principals: roles and users by ARN, never a
# whole account. Planned against a mock provider, so it needs no AWS
# account.

mock_provider "aws" {
  # A policy the provider accepts; the tests read the statements given.
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

run "roles_and_users_trusted" {
  command = plan

  variables {
    trusted_principal_arns = [
      "arn:aws:iam::123456789012:role/platform-ci",
      "arn:aws:iam::123456789012:user/ops",
    ]
  }

  assert {
    condition     = [for s in data.aws_iam_policy_document.trust.statement : s.sid] == ["AllowTrustedPrincipals"]
    error_message = "The trust policy should have one statement for the IAM principals."
  }
}

# An account's root trusts every principal in it whose own policies let it
# assume the role.
run "account_root_refused" {
  command = plan

  variables {
    trusted_principal_arns = ["arn:aws:iam::123456789012:root"]
  }

  expect_failures = [var.trusted_principal_arns]
}

run "account_id_refused" {
  command = plan

  variables {
    trusted_principal_arns = ["123456789012"]
  }

  expect_failures = [var.trusted_principal_arns]
}

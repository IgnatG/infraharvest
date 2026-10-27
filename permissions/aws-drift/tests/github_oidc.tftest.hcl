# The role's GitHub Actions trust: subjects match exactly, except
# repo:<owner>/<repository>:*, and a pattern anywhere else is refused.
# Planned against a mock provider, so it needs no AWS account.

variables {
  state_bucket = "acme-tfstate"
}

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

  mock_data "aws_iam_openid_connect_provider" {
    defaults = {
      arn = "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
    }
  }
}

run "exact_subjects_match_exactly" {
  command = plan

  variables {
    github_oidc_subjects = [
      "repo:acme/infra:ref:refs/heads/main",
      "repo:acme/infra:ref:refs/tags/v1.2.0",
      "repo:acme/infra:environment:production",
      "repo:acme/infra:pull_request",
      "repo:acme/tools:*",
    ]
  }

  assert {
    condition     = local.github_subjects.StringEquals == ["repo:acme/infra:ref:refs/heads/main", "repo:acme/infra:ref:refs/tags/v1.2.0", "repo:acme/infra:environment:production", "repo:acme/infra:pull_request"]
    error_message = "The exact subjects aren't matched with StringEquals."
  }

  assert {
    condition     = local.github_subjects.StringLike == ["repo:acme/tools:*"]
    error_message = "Only repo:<owner>/<repository>:* may be matched with StringLike."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.trust.statement : alltrue([
        for c in s.condition : c.test == (s.sid == "AllowGitHubActionsRepository" ? "StringLike" : "StringEquals")
        if c.variable == "token.actions.githubusercontent.com:sub"
      ])
    ])
    error_message = "Only the AllowGitHubActionsRepository statement may match a pattern."
  }

  assert {
    condition     = toset([for s in data.aws_iam_policy_document.trust.statement : s.sid]) == toset(["AllowGitHubActions", "AllowGitHubActionsRepository"])
    error_message = "The trust policy should have one statement for exact subjects and one for the repository pattern."
  }
}

run "no_pattern_without_one" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/infra:ref:refs/heads/main"]
  }

  assert {
    condition     = [for s in data.aws_iam_policy_document.trust.statement : s.sid] == ["AllowGitHubActions"]
    error_message = "A StringLike statement was added without a repository pattern."
  }
}

run "owner_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:*:*"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "repository_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/*:ref:refs/heads/main"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "branch_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/infra:ref:refs/heads/*"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "environment_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/infra:environment:prod?"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "other_claim_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/infra:job_workflow_ref:acme/infra/.github/workflows/import.yml@refs/heads/main"]
  }

  expect_failures = [var.github_oidc_subjects]
}

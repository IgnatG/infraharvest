# The role's GitHub Actions trust: subjects match exactly, a pattern is
# refused, and so are pull requests' workflows, as the role reads the
# state. Planned against a mock provider, so it needs no AWS account.

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
      "repo:acme@123456/infra@456789:ref:refs/heads/main",
      "repo:acme@123456/infra@456789:environment:production",
    ]
  }

  assert {
    condition     = [for s in data.aws_iam_policy_document.trust.statement : s.sid] == ["AllowGitHubActions"]
    error_message = "The trust policy should have one statement for the GitHub Actions subjects."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.trust.statement : alltrue([
        for c in s.condition : c.test == "StringEquals" && toset(c.values) == toset(["repo:acme/infra:ref:refs/heads/main", "repo:acme/infra:ref:refs/tags/v1.2.0", "repo:acme/infra:environment:production", "repo:acme@123456/infra@456789:ref:refs/heads/main", "repo:acme@123456/infra@456789:environment:production"])
        if c.variable == "token.actions.githubusercontent.com:sub"
      ])
    ])
    error_message = "The subjects aren't matched exactly."
  }
}

run "pull_request_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/infra:pull_request"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "immutable_pull_request_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme@123456/infra@456789:pull_request"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "repository_pattern_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme/infra:*"]
  }

  expect_failures = [var.github_oidc_subjects]
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

run "immutable_owner_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme@*/infra@456789:ref:refs/heads/main"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "immutable_repository_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme@123456/*:ref:refs/heads/main"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "immutable_id_not_a_number_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme@123456/infra@45678?:ref:refs/heads/main"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "immutable_owner_id_alone_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme@123456/infra:ref:refs/heads/main"]
  }

  expect_failures = [var.github_oidc_subjects]
}

run "immutable_branch_wildcard_refused" {
  command = plan

  variables {
    github_oidc_subjects = ["repo:acme@123456/infra@456789:ref:refs/heads/*"]
  }

  expect_failures = [var.github_oidc_subjects]
}

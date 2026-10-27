provider "aws" {
  region = var.region
}

data "aws_partition" "current" {}

# Trusts the principals listed, and GitHub Actions workflows whose OIDC
# token's subject matches one of github_oidc_subjects.
data "aws_iam_policy_document" "trust" {
  dynamic "statement" {
    for_each = length(var.trusted_principal_arns) > 0 ? [var.trusted_principal_arns] : []

    content {
      sid     = "AllowTrustedPrincipals"
      actions = ["sts:AssumeRole"]

      principals {
        type        = "AWS"
        identifiers = statement.value
      }
    }
  }

  # The audience is the one aws-actions/configure-aws-credentials asks for.
  # Subjects match exactly; repo:<owner>/<repository>:*, the one pattern
  # variables.tf allows, matches as a pattern, in a statement of its own.
  dynamic "statement" {
    for_each = { for test, subjects in local.github_subjects : test => subjects if length(subjects) > 0 }

    content {
      sid     = statement.key == "StringEquals" ? "AllowGitHubActions" : "AllowGitHubActionsRepository"
      actions = ["sts:AssumeRoleWithWebIdentity"]

      principals {
        type        = "Federated"
        identifiers = [local.github_oidc_provider_arn]
      }

      condition {
        test     = "StringEquals"
        variable = "token.actions.githubusercontent.com:aud"
        values   = ["sts.amazonaws.com"]
      }

      condition {
        test     = statement.key
        variable = "token.actions.githubusercontent.com:sub"
        values   = statement.value
      }
    }
  }
}

# GitHub Actions' OIDC provider. An account has one per URL: created here
# when create_github_oidc_provider is true, else the account's own is
# looked up, and only when a subject is listed.
resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_github_oidc_provider ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  tags           = var.tags
}

data "aws_iam_openid_connect_provider" "github" {
  count = !var.create_github_oidc_provider && length(var.github_oidc_subjects) > 0 ? 1 : 0

  url = "https://token.actions.githubusercontent.com"
}

locals {
  github_oidc_provider_arn = one(concat(aws_iam_openid_connect_provider.github[*].arn, data.aws_iam_openid_connect_provider.github[*].arn))

  # github_oidc_subjects by the condition that matches them.
  github_subjects = {
    StringEquals = [for subject in var.github_oidc_subjects : subject if !endswith(subject, ":*")]
    StringLike   = [for subject in var.github_oidc_subjects : subject if endswith(subject, ":*")]
  }
}

resource "aws_iam_role" "readonly" {
  name                 = var.role_name
  description          = "Read-only access for infraharvest: configuration metadata, no data-plane reads."
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = var.max_session_duration
  tags                 = var.tags

  lifecycle {
    precondition {
      condition     = length(var.trusted_principal_arns) > 0 || length(var.github_oidc_subjects) > 0
      error_message = "List at least one IAM principal ARN in trusted_principal_arns or GitHub Actions subject in github_oidc_subjects."
    }
  }
}

# Reads every service's configuration, which Terraform needs to import it.
resource "aws_iam_role_policy_attachment" "read_only_access" {
  role       = aws_iam_role.readonly.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess"
}

# ReadOnlyAccess also allows reading data (objects, items, messages, secret
# values, logs). infraharvest never needs those, so deny them explicitly.
resource "aws_iam_role_policy" "deny_data_reads" {
  name   = "deny-data-reads"
  role   = aws_iam_role.readonly.id
  policy = file("${path.module}/deny-data-reads.json")
}

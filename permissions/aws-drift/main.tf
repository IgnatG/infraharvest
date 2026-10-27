provider "aws" {
  region = var.region
}

data "aws_partition" "current" {}

locals {
  bucket_arn = "arn:${data.aws_partition.current.partition}:s3:::${var.state_bucket}"
  # The roots' state keys, <key_prefix>/<root path>/terraform.tfstate.
  state_objects = var.state_key_prefix == "" ? "${local.bucket_arn}/*" : "${local.bucket_arn}/${trim(var.state_key_prefix, "/")}/*"
  # The reads of the state, which the read-only role denies everywhere:
  # this role denies them everywhere but the state instead.
  state_reads = concat(["s3:GetObject", "s3:GetObjectTorrent", "s3:GetObjectVersion"], var.state_kms_key_arn == null ? [] : ["kms:Decrypt"])
  # The read-only role's deny on data reads, in permissions/aws.
  data_reads = jsondecode(file("${path.module}/../aws/deny-data-reads.json")).Statement[0].Action
}

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
  # Subjects match exactly: unlike permissions/aws, variables.tf allows no
  # pattern here, as the role reads the state.
  dynamic "statement" {
    for_each = length(var.github_oidc_subjects) > 0 ? [var.github_oidc_subjects] : []

    content {
      sid     = "AllowGitHubActions"
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
        test     = "StringEquals"
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
}

# Applying drift's saved refresh plan to its private copy configures the
# provider again, which calls sts:GetCallerIdentity: every principal may
# call it and no policy can deny it, so nothing here grants it, but the
# session must still be valid then (max_session_duration).
resource "aws_iam_role" "drift" {
  name                 = var.role_name
  description          = "Drift checks for infraharvest: configuration metadata and the roots' state, no other data-plane reads."
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

# Reads every service's configuration, which refreshing the roots needs.
resource "aws_iam_role_policy_attachment" "read_only_access" {
  role       = aws_iam_role.drift.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess"
}

# Reads the roots' state: lists the bucket and gets the state objects.
# Drift plans with -lock=false, so it never writes a lock.
data "aws_iam_policy_document" "read_state" {
  statement {
    sid       = "ListTheStateBucket"
    actions   = ["s3:ListBucket"]
    resources = [local.bucket_arn]
  }

  statement {
    sid       = "ReadTheRootsState"
    actions   = ["s3:GetObject"]
    resources = [local.state_objects]
  }

  dynamic "statement" {
    for_each = var.state_kms_key_arn == null ? [] : [var.state_kms_key_arn]

    content {
      sid       = "DecryptTheState"
      actions   = ["kms:Decrypt"]
      resources = [statement.value]
    }
  }
}

resource "aws_iam_role_policy" "read_state" {
  name   = "read-state"
  role   = aws_iam_role.drift.id
  policy = data.aws_iam_policy_document.read_state.json
}

# The read-only role's deny on data reads, but for the state: an explicit
# deny wins over any allow, so the state's reads are denied everywhere else.
data "aws_iam_policy_document" "deny_data_reads" {
  statement {
    sid       = "DenyDataPlaneReads"
    effect    = "Deny"
    actions   = [for action in local.data_reads : action if !contains(local.state_reads, action)]
    resources = ["*"]
  }

  statement {
    sid           = "DenyObjectReadsButTheState"
    effect        = "Deny"
    actions       = ["s3:GetObject", "s3:GetObjectTorrent", "s3:GetObjectVersion"]
    not_resources = [local.state_objects]
  }

  dynamic "statement" {
    for_each = var.state_kms_key_arn == null ? [] : [var.state_kms_key_arn]

    content {
      sid           = "DenyDecryptButTheStateKey"
      effect        = "Deny"
      actions       = ["kms:Decrypt"]
      not_resources = [statement.value]
    }
  }
}

resource "aws_iam_role_policy" "deny_data_reads" {
  name   = "deny-data-reads"
  role   = aws_iam_role.drift.id
  policy = data.aws_iam_policy_document.deny_data_reads.json
}

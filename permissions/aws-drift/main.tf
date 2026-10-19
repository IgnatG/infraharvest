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

data "aws_iam_policy_document" "trust" {
  statement {
    sid     = "AllowTrustedPrincipals"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = var.trusted_principal_arns
    }
  }
}

resource "aws_iam_role" "drift" {
  name                 = var.role_name
  description          = "Drift checks for infraharvest: configuration metadata and the roots' state, no other data-plane reads."
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = var.max_session_duration
  tags                 = var.tags
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

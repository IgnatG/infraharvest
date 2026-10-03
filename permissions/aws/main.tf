provider "aws" {
  region = var.region
}

data "aws_partition" "current" {}

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

resource "aws_iam_role" "readonly" {
  name                 = var.role_name
  description          = "Read-only access for infraharvest: configuration metadata, no data-plane reads."
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = var.max_session_duration
  tags                 = var.tags
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

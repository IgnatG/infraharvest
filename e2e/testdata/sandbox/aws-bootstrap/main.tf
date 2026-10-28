# What the AWS sandbox (the sandbox-aws workflow, TestSandboxAWS in
# e2e/sandbox_aws_test.go) needs in an account. Its owner applies it once,
# with rights to manage IAM (see e2e/README.md):
#
# - infraharvest-ci-fixture, a role that creates and destroys the estate.
#   It has PowerUserAccess, which leaves IAM out, and may manage, in IAM,
#   only the roles and instance profiles named ih-sbx-*, which the estates
#   create, and only roles that carry the boundary below. So a run can't
#   change the two roles of the sandbox, the OIDC provider, nor any other
#   role of the account, nor make a role that does more than PowerUserAccess
#   does, which is what the role does already.
# - infraharvest-ci-readonly, a role that imports the estate.
#   permissions/aws, the template users apply, makes it: ReadOnlyAccess
#   without the reads of data.
#
# Both trust the GitHub OIDC tokens of the repository's sandbox
# environment, and nothing else: no key is stored.

provider "aws" {
  region = var.region
}

data "aws_partition" "current" {}
data "aws_caller_identity" "current" {}

locals {
  partition = data.aws_partition.current.partition
  account   = data.aws_caller_identity.current.account_id
  issuer    = "token.actions.githubusercontent.com"
  tags      = { project = "infraharvest-sandbox" }
}

# The boundary of every role the estates create: what PowerUserAccess
# allows, and so what the fixture role can do already, but nothing in IAM,
# apart from the service-linked roles AWS makes for a service. A role the
# estate makes can't do more than the fixture role, and can't change who
# may sign in to the account.
data "aws_iam_policy_document" "boundary" {
  statement {
    sid         = "NotIAM"
    effect      = "Allow"
    not_actions = ["iam:*", "organizations:*", "account:*"]
    resources   = ["*"]
  }

  statement {
    sid       = "ServiceLinkedRoles"
    effect    = "Allow"
    actions   = ["iam:CreateServiceLinkedRole"]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "boundary" {
  name        = "infraharvest-sandbox-boundary"
  description = "The most a role of the infraharvest sandbox's estates can do: PowerUserAccess without IAM."
  policy      = data.aws_iam_policy_document.boundary.json
  tags        = local.tags
}

data "aws_iam_policy_document" "fixture_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = ["arn:${local.partition}:iam::${local.account}:oidc-provider/${local.issuer}"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.issuer}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.issuer}:sub"
      values   = [var.github_subject]
    }
  }
}

resource "aws_iam_role" "fixture" {
  name                 = "infraharvest-ci-fixture"
  description          = "infraharvest sandbox: creates and destroys the estate."
  assume_role_policy   = data.aws_iam_policy_document.fixture_trust.json
  max_session_duration = 7200
  tags                 = local.tags

  # The account's OIDC provider is the template's, or its own.
  depends_on = [module.readonly]
}

resource "aws_iam_role_policy_attachment" "fixture_power_user" {
  role       = aws_iam_role.fixture.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/PowerUserAccess"
}

# PowerUserAccess leaves IAM out. The estates create roles, with the
# managed policies they attach, and the instance profile of an instance,
# all named ih-sbx-*; a CloudFormation stack does the same as the caller.
data "aws_iam_policy_document" "fixture_iam" {
  # A role is made only with the boundary, and keeps it: nothing here
  # puts it, changes it or takes it off.
  statement {
    sid       = "CreateBoundedRoles"
    effect    = "Allow"
    actions   = ["iam:CreateRole"]
    resources = ["arn:${local.partition}:iam::${local.account}:role/ih-sbx-*"]

    condition {
      test     = "StringEquals"
      variable = "iam:PermissionsBoundary"
      values   = [aws_iam_policy.boundary.arn]
    }
  }

  statement {
    sid    = "ManageEstateRoles"
    effect = "Allow"
    actions = [
      "iam:AttachRolePolicy",
      "iam:DeleteRole",
      "iam:DeleteRolePolicy",
      "iam:DetachRolePolicy",
      "iam:GetRole",
      "iam:GetRolePolicy",
      "iam:ListAttachedRolePolicies",
      "iam:ListInstanceProfilesForRole",
      "iam:ListRolePolicies",
      "iam:ListRoleTags",
      "iam:PassRole",
      "iam:PutRolePolicy",
      "iam:TagRole",
      "iam:UntagRole",
      "iam:UpdateRole",
      "iam:UpdateRoleDescription",
    ]
    resources = ["arn:${local.partition}:iam::${local.account}:role/ih-sbx-*"]
  }

  statement {
    sid    = "ManageEstateInstanceProfiles"
    effect = "Allow"
    actions = [
      "iam:AddRoleToInstanceProfile",
      "iam:CreateInstanceProfile",
      "iam:DeleteInstanceProfile",
      "iam:GetInstanceProfile",
      "iam:RemoveRoleFromInstanceProfile",
      "iam:TagInstanceProfile",
      "iam:UntagInstanceProfile",
    ]
    resources = ["arn:${local.partition}:iam::${local.account}:instance-profile/ih-sbx-*"]
  }

  # Not even a role of the estate may be changed to trust anyone else, or
  # be signed in to: its trust is what it was made with.
  statement {
    sid    = "NoNewTrust"
    effect = "Deny"
    actions = [
      "iam:DeleteRolePermissionsBoundary",
      "iam:PutRolePermissionsBoundary",
      "iam:UpdateAssumeRolePolicy",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "NoSigningInToEstateRoles"
    effect    = "Deny"
    actions   = ["sts:AssumeRole", "sts:AssumeRoleWithWebIdentity", "sts:AssumeRoleWithSAML"]
    resources = ["arn:${local.partition}:iam::${local.account}:role/*"]
  }
}

resource "aws_iam_role_policy" "fixture_iam" {
  name   = "fixture-iam"
  role   = aws_iam_role.fixture.id
  policy = data.aws_iam_policy_document.fixture_iam.json
}

# The template users apply, as they apply it: the sandbox validates it.
module "readonly" {
  source = "../../../../permissions/aws"

  role_name                   = "infraharvest-ci-readonly"
  github_oidc_subjects        = [var.github_subject]
  create_github_oidc_provider = var.create_github_oidc_provider
  max_session_duration        = 7200
  region                      = var.region
  tags                        = local.tags
}

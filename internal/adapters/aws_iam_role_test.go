// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"strings"
	"testing"
)

func TestIAMRoleMapsEveryMember(t *testing.T) {
	c := cluster(t, `resource "aws_iam_role" "ci" {
  assume_role_policy    = jsonencode({ Statement = [{ Action = "sts:AssumeRole", Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" } }], Version = "2012-10-17" })
  force_detach_policies = false
  max_session_duration  = 3600
  name                  = "ci"
  name_prefix           = null
  path                  = "/build/"
  tags                  = { Team = "build" }
}
resource "aws_iam_instance_profile" "ci" {
  name = "ci"
  path = "/build/"
  role = aws_iam_role.ci.name
  tags = { Team = "build" }
}
resource "aws_iam_role_policy" "ci" {
  name   = "ci"
  policy = jsonencode({ Statement = [{ Action = "s3:GetObject", Effect = "Allow", Resource = "*" }], Version = "2012-10-17" })
  role   = aws_iam_role.ci.id
}
resource "aws_iam_role_policy_attachment" "ci_app" {
  policy_arn = aws_iam_policy.app.arn
  role       = aws_iam_role.ci.name
}
resource "aws_iam_role_policy_attachment" "ci_readonly" {
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
  role       = aws_iam_role.ci.name
}
`)
	call, err := IAMRole.Map(c)
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, IAMRole, call)
	for _, want := range []string{
		`name = "ci"`,
		"use_name_prefix = false",
		`source_trust_policy_documents = [jsonencode({ Statement = [{ Action = "sts:AssumeRole"`,
		"max_session_duration = 3600",
		`path = "/build/"`,
		`tags = { Team = "build" }`,
		"create_inline_policy = true",
		`source_inline_policy_documents = [jsonencode({ Statement = [{ Action = "s3:GetObject"`,
		"create_instance_profile = true",
		`policies = { "app" = aws_iam_policy.app.arn "ReadOnlyAccess" = "arn:aws:iam::aws:policy/ReadOnlyAccess" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "force_detach_policies") {
		t.Errorf("the module sets force_detach_policies itself:\n%s", got)
	}
	wantAddresses := map[string]string{
		"aws_iam_role.ci":                            "aws_iam_role.this[0]",
		"aws_iam_instance_profile.ci":                "aws_iam_instance_profile.this[0]",
		"aws_iam_role_policy.ci":                     "aws_iam_role_policy.inline[0]",
		"aws_iam_role_policy_attachment.ci_app":      `aws_iam_role_policy_attachment.this["app"]`,
		"aws_iam_role_policy_attachment.ci_readonly": `aws_iam_role_policy_attachment.this["ReadOnlyAccess"]`,
	}
	if !reflect.DeepEqual(call.Addresses, wantAddresses) {
		t.Errorf("addresses: got %v, want %v", call.Addresses, wantAddresses)
	}
	if call.Outputs["aws_iam_role.ci"]["id"] != "name" || call.Outputs["aws_iam_instance_profile.ci"]["arn"] != "instance_profile_arn" {
		t.Errorf("outputs: %v", call.Outputs)
	}
}

func TestIAMRoleWithoutMembers(t *testing.T) {
	c := cluster(t, `resource "aws_iam_role" "app" {
  assume_role_policy = "{}"
  name               = "app"
}
`)
	call, err := IAMRole.Map(c)
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, IAMRole, call)
	for _, unwanted := range []string{"create_inline_policy", "create_instance_profile", "policies"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("call sets %s:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "tags = null") {
		t.Errorf("an untagged role: want tags = null:\n%s", got)
	}
}

func TestIAMRoleDeclines(t *testing.T) {
	const role = `resource "aws_iam_role" "app" {
  assume_role_policy = "{}"
  name               = "app"
  tags               = { Team = "web" }
}
`
	for name, tc := range map[string]struct{ src, reason string }{
		"name prefix": {`resource "aws_iam_role" "app" {
  assume_role_policy = "{}"
  name_prefix        = "app-"
}
`, "name prefix"},
		"inline policy named otherwise": {role + `resource "aws_iam_role_policy" "app" {
  name   = "queue"
  policy = "{}"
  role   = aws_iam_role.app.id
}
`, "isn't named after the role"},
		"profile tagged otherwise": {role + `resource "aws_iam_instance_profile" "app" {
  name = "app"
  role = aws_iam_role.app.name
}
`, "another tags than the role"},
		"unmapped argument": {`resource "aws_iam_role" "app" {
  assume_role_policy = "{}"
  name               = "app"
  unknown_setting    = true
}
`, "can't set aws_iam_role.app.unknown_setting"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := IAMRole.Map(cluster(t, tc.src))
			if !IsDecline(err) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("want a decline about %q, got %v", tc.reason, err)
			}
		})
	}
}

func TestPolicyKey(t *testing.T) {
	for arn, want := range map[string]string{
		`"arn:aws:iam::aws:policy/service-role/AWSLambdaRole"`: "AWSLambdaRole",
		"aws_iam_policy.app.arn":                               "app",
		"local.policy_arn":                                     "fallback",
	} {
		c := cluster(t, "resource \"x\" \"y\" {\n  a = "+arn+"\n}\n")
		value, _ := Read("x.y", c.Anchor.Body).Attr("a")
		if got := policyKey(value, "fallback"); got != want {
			t.Errorf("policyKey(%s): got %q, want %q", arn, got, want)
		}
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// The iam-role module gives its role and its instance profile the same
// tags, from one input: accepting a change of the role's tags edits that
// input, which the profile gets too.
func TestDriftCuratedInputFeedingSeveralResources(t *testing.T) {
	t.Parallel()
	module := map[string]string{
		"main.tf":      "resource \"aws_iam_role\" \"this\" {\n  count = 1\n  tags  = var.tags\n}\n\nresource \"aws_iam_instance_profile\" \"this\" {\n  count = 1\n  tags  = var.tags\n}\n",
		"variables.tf": iamRoleModule["variables.tf"],
	}
	const role, profile = "module.deploy.aws_iam_role.this[0]", "module.deploy.aws_iam_instance_profile.this[0]"
	update, noop := tfjson.Actions{tfjson.ActionUpdate}, tfjson.Actions{tfjson.ActionNoop}
	setup := func(t *testing.T) (dir, staging string) {
		return driftModuleRoot(t, map[string]string{"main.tf": iamRoleCall}, map[string]map[string]string{"role_0a1b2c3d": module}, map[string]string{"deploy": "role_0a1b2c3d"})
	}
	opts := Options{Adapters: adapters.For("aws")}
	t.Run("only the role changed", func(t *testing.T) {
		t.Parallel()
		dir, staging := setup(t)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{moduleTags(role, "aws_iam_role", update, "prod", "dev")}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			moduleTags(role, "aws_iam_role", update, "dev", "prod"),
			moduleTags(profile, "aws_iam_instance_profile", noop, "prod", "prod"),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			moduleTags(role, "aws_iam_role", noop, "dev", "dev"),
			moduleTags(profile, "aws_iam_instance_profile", update, "prod", "dev"),
		}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, opts)
		if err != nil {
			t.Fatal(err)
		}
		c := outcomes(result)[role+" tags.Env"]
		if c.Outcome != RejectedByPlan || !strings.Contains(c.Reason, profile) {
			t.Errorf("role: %s: %s", c.Outcome, c.Reason)
		}
		if got := readFile(t, dir, "main.tf"); got != iamRoleCall {
			t.Errorf("main.tf changed:\n%s", got)
		}
	})
	t.Run("both changed alike", func(t *testing.T) {
		t.Parallel()
		dir, staging := setup(t)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
			moduleTags(role, "aws_iam_role", update, "prod", "dev"),
			moduleTags(profile, "aws_iam_instance_profile", update, "prod", "dev"),
		}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			moduleTags(role, "aws_iam_role", update, "dev", "prod"),
			moduleTags(profile, "aws_iam_instance_profile", update, "dev", "prod"),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			moduleTags(role, "aws_iam_role", noop, "dev", "dev"),
			moduleTags(profile, "aws_iam_instance_profile", noop, "dev", "dev"),
		}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range result.Changes {
			if c.Outcome != Written {
				t.Errorf("%s: %s: %s", c.Address, c.Outcome, c.Reason)
			}
		}
		if got, want := readFile(t, dir, "main.tf"), strings.Replace(iamRoleCall, "prod", "dev", 1); got != want {
			t.Errorf("main.tf:\n%s", got)
		}
	})
}

// A key the cloud removed from the VPC alone, which the module gives it from
// the call's tags as it gives it to every resource, can't be taken out of
// vpc_tags, its own input, where it isn't: a hand edit, whose reason says
// where the key is.
func TestDriftCuratedRemovedKeyOfSharedInput(t *testing.T) {
	t.Parallel()
	call := `module "net" {
  source  = "` + adapters.VPC().Source + `"
  version = "` + adapters.VPC().Version + `"

  name = "net"
  tags = {
    Env  = "prod"
    Team = "net"
  }
  vpc_tags = {
    Name = "net"
  }
}
`
	module := map[string]string{
		"main.tf":      "resource \"aws_vpc\" \"this\" {\n  count = 1\n  tags  = merge(var.tags, var.vpc_tags)\n}\n\nresource \"aws_internet_gateway\" \"this\" {\n  count = 1\n  tags  = merge(var.tags, var.igw_tags)\n}\n",
		"variables.tf": "variable \"tags\" {\n  type = map(string)\n  default = {}\n}\nvariable \"vpc_tags\" {\n  type = map(string)\n  default = {}\n}\nvariable \"igw_tags\" {\n  type = map(string)\n  default = {}\n}\n",
	}
	vpc, igw := "module.net.aws_vpc.this[0]", "module.net.aws_internet_gateway.this[0]"
	dir, staging := driftModuleRoot(t, map[string]string{"main.tf": call}, map[string]map[string]string{"vpc_0a1b2c3d": module}, map[string]string{"net": "vpc_0a1b2c3d"})
	upd, noop := tfjson.Actions{tfjson.ActionUpdate}, tfjson.Actions{tfjson.ActionNoop}
	tags := func(env bool) map[string]any {
		if env {
			return map[string]any{"tags": tagMap("Env", "prod", "Team", "net", "Name", "net")}
		}
		return map[string]any{"tags": tagMap("Team", "net", "Name", "net")}
	}
	full := map[string]any{"tags": tagMap("Env", "prod", "Team", "net")}
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{driftResource(vpc, "aws_vpc", upd, tags(true), tags(false))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{driftResource(vpc, "aws_vpc", upd, tags(false), tags(true)), driftResource(igw, "aws_internet_gateway", noop, full, full)}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code}, Options{Adapters: adapters.For("aws")})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)[vpc+" tags.Env"]
	if len(result.Changes) != 1 || c.Outcome != Manual || !strings.Contains(c.Reason, "vpc_tags") || !strings.Contains(c.Reason, "argument tags has the key") {
		t.Errorf("changes %+v", result.Changes)
	}
	if got := readFile(t, dir, "main.tf"); got != call {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

// The project IAM module has no Inverse entries: what a grant's attributes
// are is told by the keys of its inputs, which no input path names, so a
// change of one is a hand edit.
func TestDriftProjectIAMGrantIsAHandEdit(t *testing.T) {
	t.Parallel()
	call := `module "project_iam" {
  source  = "` + adapters.GoogleProjectIAM().Source + `"
  version = "` + adapters.GoogleProjectIAM().Version + `"

  projects = ["p"]
  bindings = {
    "roles/viewer" = ["user:a@example.com"]
  }
}
`
	module := map[string]string{
		"main.tf":      "resource \"google_project_iam_member\" \"project_iam_additive\" {\n  for_each = local.x\n  project = each.value.project\n  role = each.value.role\n  member = each.value.member\n}\nlocals { x = {} }\n",
		"variables.tf": "variable \"projects\" {\n  type = list(string)\n}\nvariable \"bindings\" {\n  type = map(list(string))\n}\n",
	}
	addr := `module.project_iam.google_project_iam_member.project_iam_additive["default--roles/viewer--user:a@example.com"]`
	dir, staging := driftModuleRoot(t, map[string]string{"main.tf": call}, map[string]map[string]string{"iam_0a1b2c3d": module}, map[string]string{"project_iam": "iam_0a1b2c3d"})
	upd := tfjson.Actions{tfjson.ActionUpdate}
	v := func(role string) map[string]any { return map[string]any{"role": role, "project": "p"} }
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{driftResource(addr, "google_project_iam_member", upd, v("roles/viewer"), v("roles/editor"))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{driftResource(addr, "google_project_iam_member", upd, v("roles/editor"), v("roles/viewer"))}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code}, Options{Adapters: adapters.For("google")})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)[addr+" role"]
	if len(result.Changes) != 1 || c.Outcome != Manual || !strings.Contains(c.Reason, "by hand") {
		t.Errorf("changes %+v", result.Changes)
	}
	if got := readFile(t, dir, "main.tf"); got != call {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

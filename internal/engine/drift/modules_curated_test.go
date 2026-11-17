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

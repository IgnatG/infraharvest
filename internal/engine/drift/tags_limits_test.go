// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// A resource that gets a default tag, and changes it in the cloud to a
// value of its own, reports the provider's value as what it was: the
// resource's own tags had no such key, tags_all had it.
func TestDriftDefaultTagChangeKeepsTheValueItHad(t *testing.T) {
	t.Parallel()
	dir, staging := awsRoot(t)
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("dev"))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsLogs("prod")),
		tagNoop("aws_sqs_queue.jobs", awsJobs("prod")),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("aws_s3_bucket.logs", awsLogs("dev")), tagNoop("aws_sqs_queue.jobs", awsJobs("prod"))}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)["aws_s3_bucket.logs tags.Env"]
	if c.Before != "prod" || c.After != "dev" {
		t.Errorf("tags.Env went from %v to %v, want prod to dev", c.Before, c.After)
	}
}

const azureTwoMain = `resource "azurerm_resource_group" "app" {
  name     = "app"
  location = "uksouth"
  tags     = local.tags
}

resource "azurerm_storage_account" "logs" {
  name = "logs"
  tags = merge(local.tags, { Name = "logs" })
}
`

// A key the cloud added to every resource that gets the local's tags is
// one change of the local, accepted by adding the key to it once.
func TestDriftSharesAnAddedTag(t *testing.T) {
	t.Parallel()
	app := func(owner string) map[string]any {
		if owner == "" {
			return map[string]any{"tags": tagMap("Env", "prod")}
		}
		return map[string]any{"tags": tagMap("Env", "prod", "Owner", owner)}
	}
	logs := func(owner string) map[string]any {
		if owner == "" {
			return map[string]any{"tags": tagMap("Env", "prod", "Name", "logs")}
		}
		return map[string]any{"tags": tagMap("Env", "prod", "Name", "logs", "Owner", owner)}
	}
	dir, staging := acceptRoot(t, map[string]string{"main.tf": azureTwoMain, "locals.tf": azureLocals})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagUpdate("azurerm_resource_group.app", app(""), app("ops")),
		tagUpdate("azurerm_storage_account.logs", logs(""), logs("ops")),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("azurerm_resource_group.app", app("ops"), app("")),
		tagUpdate("azurerm_storage_account.logs", logs("ops"), logs("")),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("azurerm_resource_group.app", app("ops")), tagNoop("azurerm_storage_account.logs", logs("ops"))}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 1 {
		t.Fatalf("changes %+v, want one of local.tags", result.Changes)
	}
	c := result.Changes[0]
	if c.Address != "local.tags" || c.Attribute != "tags.Owner" || c.Outcome != Written || c.Before != nil || c.After != "ops" {
		t.Errorf("change %s %s: %s (%s), %v to %v", c.Address, c.Attribute, c.Outcome, c.Reason, c.Before, c.After)
	}
	if want := []string{"azurerm_resource_group.app", "azurerm_storage_account.logs"}; !reflect.DeepEqual(c.Covers, want) {
		t.Errorf("covers %v, want %v", c.Covers, want)
	}
	if got, want := readFile(t, dir, "locals.tf"), "locals {\n  tags = {\n    Env   = \"prod\"\n    Owner = \"ops\"\n  }\n}\n"; got != want {
		t.Errorf("locals.tf:\n%s", got)
	}
	if got := readFile(t, dir, "main.tf"); got != azureTwoMain {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

// The same for the AWS provider's default tags, where the resources that
// get a key are told from the state; one the cloud added to only some of
// them stays a change of each.
func TestDriftSharesAnAddedDefaultTag(t *testing.T) {
	t.Parallel()
	logs := func(owner bool) map[string]any {
		if owner {
			return awsTags(tagMap("Owner", "ops"), tagMap("Env", "prod", "Owner", "ops", "Team", "data"))
		}
		return awsLogs("prod")
	}
	jobs := func(owner bool) map[string]any {
		if owner {
			return awsTags(tagMap("Name", "jobs", "Owner", "ops"), tagMap("Env", "prod", "Name", "jobs", "Owner", "ops", "Team", "data"))
		}
		return awsJobs("prod")
	}
	t.Run("every resource", func(t *testing.T) {
		t.Parallel()
		dir, staging := awsRoot(t)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
			tagUpdate("aws_s3_bucket.logs", logs(false), logs(true)),
			tagUpdate("aws_sqs_queue.jobs", jobs(false), jobs(true)),
		}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("aws_s3_bucket.logs", logs(true), logs(false)),
			tagUpdate("aws_sqs_queue.jobs", jobs(true), jobs(false)),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("aws_s3_bucket.logs", logs(true)), tagNoop("aws_sqs_queue.jobs", jobs(true))}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Changes) != 1 {
			t.Fatalf("changes %+v, want one of local.tags", result.Changes)
		}
		c := result.Changes[0]
		if c.Address != "local.tags" || c.Attribute != "tags.Owner" || c.Outcome != Written || c.Before != nil || c.After != "ops" {
			t.Errorf("change %s %s: %s (%s), %v to %v", c.Address, c.Attribute, c.Outcome, c.Reason, c.Before, c.After)
		}
		if want := []string{"aws_s3_bucket.logs", "aws_sqs_queue.jobs"}; !reflect.DeepEqual(c.Covers, want) {
			t.Errorf("covers %v, want %v", c.Covers, want)
		}
		if got, want := readFile(t, dir, "locals.tf"), "locals {\n  tags = {\n    Env   = \"prod\"\n    Team  = \"data\"\n    Owner = \"ops\"\n  }\n}\n"; got != want {
			t.Errorf("locals.tf:\n%s", got)
		}
		if got := readFile(t, dir, "main.tf"); got != awsMain {
			t.Errorf("main.tf changed:\n%s", got)
		}
	})
	t.Run("some resources", func(t *testing.T) {
		t.Parallel()
		dir, staging := awsRoot(t)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("aws_s3_bucket.logs", logs(false), logs(true))}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("aws_s3_bucket.logs", logs(true), logs(false)),
			tagNoop("aws_sqs_queue.jobs", jobs(false)),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("aws_s3_bucket.logs", logs(true)), tagNoop("aws_sqs_queue.jobs", jobs(false))}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		c := outcomes(result)["aws_s3_bucket.logs tags.Owner"]
		if len(result.Changes) != 1 || c.Outcome != Written || len(c.Covers) > 0 {
			t.Errorf("changes %+v", result.Changes)
		}
		if got := readFile(t, dir, "locals.tf"); got != awsLocals {
			t.Errorf("locals.tf changed:\n%s", got)
		}
		if main := readFile(t, dir, "main.tf"); !strings.Contains(main, `Owner = "ops"`) {
			t.Errorf("main.tf:\n%s", main)
		}
	})
}

// A key added to some of the resources that get the local's tags is
// accepted at each: adding it to the local would reach the others.
func TestDriftKeepsAnAddedTagPerResource(t *testing.T) {
	t.Parallel()
	app := func(owner bool) map[string]any {
		if owner {
			return map[string]any{"tags": tagMap("Env", "prod", "Owner", "ops")}
		}
		return map[string]any{"tags": tagMap("Env", "prod")}
	}
	logs := map[string]any{"tags": tagMap("Env", "prod", "Name", "logs")}
	dir, staging := acceptRoot(t, map[string]string{"main.tf": azureTwoMain, "locals.tf": azureLocals})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("azurerm_resource_group.app", app(false), app(true))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("azurerm_resource_group.app", app(true), app(false)),
		tagNoop("azurerm_storage_account.logs", logs),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("azurerm_resource_group.app", app(true)), tagNoop("azurerm_storage_account.logs", logs)}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)["azurerm_resource_group.app tags.Owner"]
	if len(result.Changes) != 1 || c.Outcome != Written || len(c.Covers) > 0 {
		t.Errorf("changes %+v", result.Changes)
	}
	if got := readFile(t, dir, "locals.tf"); got != azureLocals {
		t.Errorf("locals.tf changed:\n%s", got)
	}
}

// A key of the local's tags removed from only some of the resources that
// get it takes a hand edit: merge() can't take a key out, and a resource
// can't leave out one of the provider's default tags (see
// TestDriftDefaultTagsRemoved). The reason says why.
func TestDriftRemovedLocalTagFromSomeIsAHandEdit(t *testing.T) {
	t.Parallel()
	app := func(env bool) map[string]any {
		if env {
			return map[string]any{"tags": tagMap("Env", "prod")}
		}
		return map[string]any{"tags": nil}
	}
	logs := map[string]any{"tags": tagMap("Env", "prod", "Name", "logs")}
	dir, staging := acceptRoot(t, map[string]string{"main.tf": azureTwoMain, "locals.tf": azureLocals})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("azurerm_resource_group.app", app(true), app(false))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("azurerm_resource_group.app", app(false), app(true)),
		tagNoop("azurerm_storage_account.logs", logs),
	}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)["azurerm_resource_group.app tags.Env"]
	if c.Outcome != Manual || !strings.Contains(c.Reason, "local.tags") || !strings.Contains(c.Reason, "by hand") {
		t.Errorf("app: %s: %s", c.Outcome, c.Reason)
	}
	if got := readFile(t, dir, "locals.tf"); got != azureLocals {
		t.Errorf("locals.tf changed:\n%s", got)
	}
	if got := readFile(t, dir, "main.tf"); got != azureTwoMain {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

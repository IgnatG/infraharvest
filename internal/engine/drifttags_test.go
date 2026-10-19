// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// tagMap is a map of tags, from keys and values in turn; nil for none.
func tagMap(pairs ...string) any {
	if len(pairs) == 0 {
		return nil
	}
	m := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

// awsTags are an AWS resource's values: its own tags, and tags_all, with
// the provider's default tags.
func awsTags(own, all any) map[string]any {
	return map[string]any{"tags": own, "tags_all": all}
}

// googleLabelValues are a Google resource's values: its own labels, and
// terraform_labels and effective_labels, with the provider's.
func googleLabelValues(own, all any) map[string]any {
	return map[string]any{"labels": own, "terraform_labels": all, "effective_labels": all}
}

func tagUpdate(address string, before, after map[string]any) *tfjson.ResourceChange {
	typ, _, _ := strings.Cut(address, ".")
	return driftResource(address, typ, tfjson.Actions{tfjson.ActionUpdate}, before, after)
}

func tagNoop(address string, values map[string]any) *tfjson.ResourceChange {
	typ, _, _ := strings.Cut(address, ".")
	return driftResource(address, typ, tfjson.Actions{tfjson.ActionNoop}, values, values)
}

const defaultTagsProviders = `provider "aws" {
  region = "eu-west-2"
  default_tags {
    tags = local.tags
  }
}
`

const awsLocals = `locals {
  tags = {
    Env  = "prod"
    Team = "data"
  }
}
`

const awsMain = `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
}

resource "aws_sqs_queue" "jobs" {
  name = "jobs"
  tags = {
    Name = "jobs"
  }
}
`

func awsRoot(t *testing.T) (dir, staging string) {
	t.Helper()
	return acceptRoot(t, map[string]string{"providers.tf": defaultTagsProviders, "locals.tf": awsLocals, "main.tf": awsMain})
}

// The AWS resources' values before and after Env changes from prod to
// env in the cloud, or is removed ("").
func awsLogs(env string) map[string]any {
	if env == "" {
		return awsTags(nil, tagMap("Team", "data"))
	}
	if env == "prod" {
		return awsTags(nil, tagMap("Env", env, "Team", "data"))
	}
	return awsTags(tagMap("Env", env), tagMap("Env", env, "Team", "data"))
}

func awsJobs(env string) map[string]any {
	if env == "" {
		return awsTags(tagMap("Name", "jobs"), tagMap("Name", "jobs", "Team", "data"))
	}
	if env == "prod" {
		return awsTags(tagMap("Name", "jobs"), tagMap("Env", env, "Name", "jobs", "Team", "data"))
	}
	return awsTags(tagMap("Env", env, "Name", "jobs"), tagMap("Env", env, "Name", "jobs", "Team", "data"))
}

// A key of the AWS provider's default_tags every resource changed the
// same way is one change of local.tags, accepted by editing it once,
// although the plan of the edit, without refreshing, still changes each
// resource's own tags: tags_all, what reaches the cloud, stays.
func TestDriftSharesDefaultTags(t *testing.T) {
	t.Parallel()
	dir, staging := awsRoot(t)
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("dev")),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("prod"), awsJobs("dev")),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsLogs("prod")),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("dev"), awsJobs("prod")),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsTags(nil, tagMap("Env", "dev", "Team", "data"))),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("dev"), awsTags(tagMap("Name", "jobs"), tagMap("Env", "dev", "Name", "jobs", "Team", "data"))),
	}}
	result, copied, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 1 {
		t.Fatalf("changes %+v, want one of local.tags", result.Changes)
	}
	c := result.Changes[0]
	if c.Address != "local.tags" || c.Attribute != "tags.Env" || c.Outcome != DriftWritten || c.Before != "prod" || c.After != "dev" {
		t.Errorf("change %s %s: %s (%s), %v → %v", c.Address, c.Attribute, c.Outcome, c.Reason, c.Before, c.After)
	}
	if want := []string{"aws_s3_bucket.logs", "aws_sqs_queue.jobs"}; !reflect.DeepEqual(c.Covers, want) {
		t.Errorf("covers %v, want %v", c.Covers, want)
	}
	if c.Source == nil || c.Source.File != "locals.tf" || c.Source.Line != 3 {
		t.Errorf("source %+v", c.Source)
	}
	if got, want := readFile(t, dir, "locals.tf"), strings.Replace(awsLocals, `"prod"`, `"dev"`, 1); got != want {
		t.Errorf("locals.tf:\n%s\nwant:\n%s", got, want)
	}
	for name, want := range map[string]string{"main.tf": awsMain, "providers.tf": defaultTagsProviders} {
		if got := readFile(t, dir, name); got != want {
			t.Errorf("%s changed:\n%s", name, got)
		}
	}
	if len(copied.planOpts) != 3 {
		t.Errorf("%d plans, want 3", len(copied.planOpts))
	}
}

// A key of the default tags only some resources changed is accepted at
// each, in its own tags, whose value wins over the provider's.
func TestDriftDefaultTagsSomeResources(t *testing.T) {
	t.Parallel()
	dir, staging := awsRoot(t)
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("dev"))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsLogs("prod")),
		tagNoop("aws_sqs_queue.jobs", awsJobs("prod")),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("aws_s3_bucket.logs", awsLogs("dev")), tagNoop("aws_sqs_queue.jobs", awsJobs("prod"))}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	if c := got["aws_s3_bucket.logs tags.Env"]; c.Outcome != DriftWritten || len(c.Covers) > 0 {
		t.Errorf("logs: %s (%s), covers %v", c.Outcome, c.Reason, c.Covers)
	}
	if len(result.Changes) != 1 {
		t.Errorf("changes %+v", result.Changes)
	}
	main := readFile(t, dir, "main.tf")
	if !strings.Contains(main, `Env = "dev"`) || strings.Count(main, "Env") != 1 {
		t.Errorf("main.tf:\n%s", main)
	}
	if got := readFile(t, dir, "locals.tf"); got != awsLocals {
		t.Errorf("locals.tf changed:\n%s", got)
	}
}

// A key of the default tags removed from every resource is removed from
// local.tags; removed from some, it takes a hand edit, as a resource
// can't leave out one of the provider's tags.
func TestDriftDefaultTagsRemoved(t *testing.T) {
	t.Parallel()
	t.Run("from every resource", func(t *testing.T) {
		t.Parallel()
		dir, staging := awsRoot(t)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
			tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("")),
			tagUpdate("aws_sqs_queue.jobs", awsJobs("prod"), awsJobs("")),
		}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("aws_s3_bucket.logs", awsLogs(""), awsLogs("prod")),
			tagUpdate("aws_sqs_queue.jobs", awsJobs(""), awsJobs("prod")),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("aws_s3_bucket.logs", awsLogs("")), tagNoop("aws_sqs_queue.jobs", awsJobs(""))}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Changes) != 1 || result.Changes[0].Address != "local.tags" || result.Changes[0].Outcome != DriftWritten || result.Changes[0].After != nil {
			t.Fatalf("changes %+v", result.Changes)
		}
		want := "locals {\n  tags = {\n    Team = \"data\"\n  }\n}\n"
		if got := readFile(t, dir, "locals.tf"); got != want {
			t.Errorf("locals.tf:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("from some", func(t *testing.T) {
		t.Parallel()
		dir, staging := awsRoot(t)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs(""))}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("aws_s3_bucket.logs", awsLogs(""), awsLogs("prod")),
			tagNoop("aws_sqs_queue.jobs", awsJobs("prod")),
		}}
		result, copied, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code}, DriftOptions{})
		if err != nil {
			t.Fatal(err)
		}
		c := outcomes(result)["aws_s3_bucket.logs tags.Env"]
		if c.Outcome != DriftManual || !strings.Contains(c.Reason, "default_tags") || !strings.Contains(c.Reason, "local.tags") {
			t.Errorf("logs: %s: %s", c.Outcome, c.Reason)
		}
		if len(copied.planOpts) != 2 {
			t.Errorf("%d plans, want the refresh and the code's only", len(copied.planOpts))
		}
		if got := readFile(t, dir, "locals.tf"); got != awsLocals {
			t.Errorf("locals.tf changed:\n%s", got)
		}
	})
}

// A shared edit that reaches a resource the change doesn't cover is
// caught by that resource's plan: the shared edit is left out, and each
// covered resource is edited on its own.
func TestDriftSharedEditLeaks(t *testing.T) {
	t.Parallel()
	dir, staging := awsRoot(t)
	// The state of extra doesn't tell it gets the provider's tags.
	extra := map[string]any{"bucket": "extra"}
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("dev")),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("prod"), awsJobs("dev")),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsLogs("prod")),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("dev"), awsJobs("prod")),
		tagNoop("aws_s3_bucket.extra", extra),
	}}
	shared := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsTags(nil, tagMap("Env", "dev", "Team", "data"))),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("dev"), awsTags(tagMap("Name", "jobs"), tagMap("Env", "dev", "Name", "jobs", "Team", "data"))),
		tagUpdate("aws_s3_bucket.extra", extra, map[string]any{"bucket": "extra", "tags_all": tagMap("Env", "dev")}),
	}}
	each := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagNoop("aws_s3_bucket.logs", awsLogs("dev")),
		tagNoop("aws_sqs_queue.jobs", awsJobs("dev")),
		tagNoop("aws_s3_bucket.extra", extra),
	}}
	result, copied, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, shared, each}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	if len(result.Changes) != 2 {
		t.Fatalf("changes %+v, want each resource's", result.Changes)
	}
	for _, key := range []string{"aws_s3_bucket.logs tags.Env", "aws_sqs_queue.jobs tags.Env"} {
		if c := got[key]; c.Outcome != DriftWritten || c.Decision != DriftAccept || !strings.Contains(c.Reason, "aws_s3_bucket.extra") {
			t.Errorf("%s: %s (%s): %s", key, c.Outcome, c.Decision, c.Reason)
		}
	}
	if got := readFile(t, dir, "locals.tf"); got != awsLocals {
		t.Errorf("locals.tf changed:\n%s", got)
	}
	if main := readFile(t, dir, "main.tf"); strings.Count(main, `Env  = "dev"`)+strings.Count(main, `Env = "dev"`) != 2 {
		t.Errorf("main.tf:\n%s", main)
	}
	if len(copied.planOpts) != 4 {
		t.Errorf("%d plans, want 4", len(copied.planOpts))
	}
}

// A shared change the rules keep is kept, once.
func TestDriftKeepsSharedTags(t *testing.T) {
	t.Parallel()
	dir, staging := awsRoot(t)
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("dev")),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("prod"), awsJobs("dev")),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsLogs("prod")),
		tagUpdate("aws_sqs_queue.jobs", awsJobs("dev"), awsJobs("prod")),
	}}
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{refresh, code}}
	var seen []DriftChange
	accept := func(c DriftChange) bool {
		seen = append(seen, c)
		return len(c.Covers) == 0
	}
	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, DriftOptions{Accept: accept})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Address != "local.tags" {
		t.Errorf("the rules saw %+v", seen)
	}
	if len(result.Changes) != 1 || result.Changes[0].Outcome != DriftKept || result.Changes[0].Decision != DriftKeep {
		t.Errorf("changes %+v", result.Changes)
	}
	if got := readFile(t, dir, "locals.tf"); got != awsLocals {
		t.Errorf("locals.tf changed:\n%s", got)
	}
}

const azureMain = `resource "azurerm_resource_group" "app" {
  name     = "app"
  location = "uksouth"
  tags     = local.tags
}

resource "azurerm_storage_account" "logs" {
  name = "logs"
  tags = merge(local.tags, { Name = "logs" })
}

resource "azurerm_key_vault" "own" {
  name = "own"
  tags = merge(local.tags, { Env = "test" })
}
`

const azureLocals = `locals {
  tags = {
    Env = "prod"
  }
}
`

// Azure's resources refer to local.tags, merging their own tags over it:
// a key every resource that gets it changed the same way is accepted in
// local.tags once; changed on some, at each, merged over the local.
func TestDriftSharesLocalTags(t *testing.T) {
	t.Parallel()
	app := func(env string) map[string]any { return map[string]any{"tags": tagMap("Env", env)} }
	logs := func(env string) map[string]any { return map[string]any{"tags": tagMap("Env", env, "Name", "logs")} }
	own := tagNoop("azurerm_key_vault.own", map[string]any{"tags": tagMap("Env", "test")})
	t.Run("every resource", func(t *testing.T) {
		t.Parallel()
		dir, staging := acceptRoot(t, map[string]string{"main.tf": azureMain, "locals.tf": azureLocals})
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
			tagUpdate("azurerm_resource_group.app", app("prod"), app("dev")),
			tagUpdate("azurerm_storage_account.logs", logs("prod"), logs("dev")),
		}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("azurerm_resource_group.app", app("dev"), app("prod")),
			tagUpdate("azurerm_storage_account.logs", logs("dev"), logs("prod")),
			own,
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("azurerm_resource_group.app", app("dev")), tagNoop("azurerm_storage_account.logs", logs("dev")), own}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Changes) != 1 {
			t.Fatalf("changes %+v", result.Changes)
		}
		c := result.Changes[0]
		if c.Address != "local.tags" || c.Outcome != DriftWritten || !reflect.DeepEqual(c.Covers, []string{"azurerm_resource_group.app", "azurerm_storage_account.logs"}) {
			t.Errorf("change %s: %s (%s), covers %v", c.Address, c.Outcome, c.Reason, c.Covers)
		}
		if got, want := readFile(t, dir, "locals.tf"), strings.Replace(azureLocals, "prod", "dev", 1); got != want {
			t.Errorf("locals.tf:\n%s", got)
		}
		if got := readFile(t, dir, "main.tf"); got != azureMain {
			t.Errorf("main.tf changed:\n%s", got)
		}
	})
	t.Run("some resources", func(t *testing.T) {
		t.Parallel()
		dir, staging := acceptRoot(t, map[string]string{"main.tf": azureMain, "locals.tf": azureLocals})
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("azurerm_resource_group.app", app("prod"), app("dev"))}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("azurerm_resource_group.app", app("dev"), app("prod")),
			tagNoop("azurerm_storage_account.logs", logs("prod")),
			own,
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("azurerm_resource_group.app", app("dev")), tagNoop("azurerm_storage_account.logs", logs("prod")), own}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if c := outcomes(result)["azurerm_resource_group.app tags.Env"]; c.Outcome != DriftWritten {
			t.Errorf("app: %s: %s", c.Outcome, c.Reason)
		}
		if main := readFile(t, dir, "main.tf"); !strings.Contains(main, `merge(local.tags, { Env = "dev" })`) {
			t.Errorf("main.tf:\n%s", main)
		}
		if got := readFile(t, dir, "locals.tf"); got != azureLocals {
			t.Errorf("locals.tf changed:\n%s", got)
		}
	})
}

// A local something else refers to, such as an output, isn't shared:
// editing it would reach further than the resources' tags.
func TestSharedTagSources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"aws", map[string]string{"providers.tf": defaultTagsProviders, "locals.tf": awsLocals, "main.tf": awsMain}, []string{"local.tags"}},
		{"azure", map[string]string{"locals.tf": azureLocals, "main.tf": azureMain}, []string{"local.tags"}},
		{"an output", map[string]string{"locals.tf": azureLocals, "main.tf": azureMain, "outputs.tf": "output \"tags\" {\n  value = local.tags\n}\n"}, nil},
		{"one resource", map[string]string{"locals.tf": azureLocals, "main.tf": "resource \"azurerm_resource_group\" \"app\" {\n  tags = local.tags\n}\n"}, nil},
		{"a literal", map[string]string{"providers.tf": "provider \"aws\" {\n  default_tags {\n    tags = {\n      Env = \"prod\"\n    }\n  }\n}\n"}, []string{"provider.aws.default_tags"}},
		{"an alias", map[string]string{"locals.tf": awsLocals, "providers.tf": defaultTagsProviders + "\nprovider \"aws\" {\n  alias = \"us\"\n  default_tags {\n    tags = local.tags\n  }\n}\n"}, nil},
		{"google", map[string]string{"locals.tf": googleLocals, "providers.tf": googleProviders}, []string{"local.labels"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseDriftConfig(byteFiles(tc.files))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range sharedTagSources(cfg) {
				got = append(got, s.address)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

const googleProviders = `provider "google" {
  project        = "p"
  default_labels = local.labels
}
`

const googleLocals = `locals {
  labels = {
    env = "prod"
  }
}
`

const googleMain = `resource "google_storage_bucket" "a" {
  name = "a"
}

resource "google_storage_bucket" "b" {
  name = "b"
}
`

// Google's default_labels: a label every resource changed the same way
// is accepted in local.labels once; changed on some, in each resource's
// own labels, which the plan of the edit, without refreshing, records in
// the state while what reaches the cloud stays.
func TestDriftSharesDefaultLabels(t *testing.T) {
	t.Parallel()
	files := map[string]string{"providers.tf": googleProviders, "locals.tf": googleLocals, "main.tf": googleMain}
	t.Run("every resource", func(t *testing.T) {
		t.Parallel()
		dir, staging := acceptRoot(t, files)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
			tagUpdate("google_storage_bucket.a", googleLabelValues(nil, tagMap("env", "prod")), googleLabelValues(nil, tagMap("env", "dev"))),
			tagUpdate("google_storage_bucket.b", googleLabelValues(nil, tagMap("env", "prod")), googleLabelValues(nil, tagMap("env", "dev"))),
		}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("google_storage_bucket.a", googleLabelValues(nil, tagMap("env", "dev")), googleLabelValues(nil, tagMap("env", "prod"))),
			tagUpdate("google_storage_bucket.b", googleLabelValues(nil, tagMap("env", "dev")), googleLabelValues(nil, tagMap("env", "prod"))),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagNoop("google_storage_bucket.a", googleLabelValues(nil, tagMap("env", "dev"))),
			tagNoop("google_storage_bucket.b", googleLabelValues(nil, tagMap("env", "dev"))),
		}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Changes) != 1 {
			t.Fatalf("changes %+v", result.Changes)
		}
		if c := result.Changes[0]; c.Address != "local.labels" || c.Attribute != "labels.env" || c.Outcome != DriftWritten || len(c.Covers) != 2 {
			t.Errorf("change %+v", c)
		}
		if got, want := readFile(t, dir, "locals.tf"), strings.Replace(googleLocals, "prod", "dev", 1); got != want {
			t.Errorf("locals.tf:\n%s", got)
		}
	})
	t.Run("some resources", func(t *testing.T) {
		t.Parallel()
		dir, staging := acceptRoot(t, files)
		refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
			tagUpdate("google_storage_bucket.a", googleLabelValues(nil, tagMap("env", "prod")), googleLabelValues(nil, tagMap("env", "dev"))),
		}}
		code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("google_storage_bucket.a", googleLabelValues(nil, tagMap("env", "dev")), googleLabelValues(nil, tagMap("env", "prod"))),
			tagNoop("google_storage_bucket.b", googleLabelValues(nil, tagMap("env", "prod"))),
		}}
		edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			tagUpdate("google_storage_bucket.a", googleLabelValues(nil, tagMap("env", "dev")), googleLabelValues(tagMap("env", "dev"), tagMap("env", "dev"))),
			tagNoop("google_storage_bucket.b", googleLabelValues(nil, tagMap("env", "prod"))),
		}}
		result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, DriftOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if c := outcomes(result)["google_storage_bucket.a labels.env"]; c.Outcome != DriftWritten {
			t.Errorf("a: %s: %s", c.Outcome, c.Reason)
		}
		if main := readFile(t, dir, "main.tf"); !strings.Contains(main, `env = "dev"`) {
			t.Errorf("main.tf:\n%s", main)
		}
		if got := readFile(t, dir, "locals.tf"); got != googleLocals {
			t.Errorf("locals.tf changed:\n%s", got)
		}
	})
}

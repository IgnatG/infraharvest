// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// stackScopedProvider is a provider scoped to the stack app, which has the
// bucket app-assets and nothing else of what is listed.
type stackScopedProvider struct {
	fakeProvider
}

func (*stackScopedProvider) ImportScope(_ context.Context, resources []harvest.Resource) (*harvest.ImportScope, error) {
	s := &harvest.ImportScope{Inside: map[string]bool{}, Outside: map[string]string{}}
	for _, r := range resources {
		key := r.Address.Type + " " + r.Observed.ID
		if r.Observed.ID == "app-assets" {
			s.Inside[key] = true
		} else {
			s.Outside[key] = report.OutsideScopePrefix + "not a resource of CloudFormation stack app (--cfn-stack)"
		}
	}
	s.Stacks = []report.CloudFormationStack{{Name: "app", ID: "stack/app", Resources: []report.CloudFormationResource{
		{LogicalID: "Assets", Type: "AWS::S3::Bucket", Matches: []report.CloudFormationMatch{{Type: "aws_s3_bucket", ID: "app-assets"}}},
	}}}
	return s, nil
}

// The scope takes the stack's bucket, though the defaults leave it out as
// CloudFormation's, by the stacks or by its tags, and leaves out the rest,
// though a selection file's rule includes everything; the stack goes into
// the report, its bucket in the call's scope.
func TestImportScope(t *testing.T) {
	const scope = "aws/123456789012/eu-west-2"
	listed := listing{resources: map[string][]harvest.Resource{"s3": {
		harvest.RecordBare("app-assets", "app-assets", "aws_s3_bucket", "aws"),
		harvest.RecordBare("other", "other", "aws_s3_bucket", "aws"),
	}}}
	defaults := map[string]string{"aws_s3_bucket app-assets": report.OtherToolPrefix + "CloudFormation stack app (per its tag aws:cloudformation:stack-name=app; --include-owned imports it)"}
	run := NewRun()

	defaults, err := run.applyImportScope(t.Context(), &stackScopedProvider{}, &listed, defaults, scope)
	if err != nil {
		t.Fatal(err)
	}

	outside := report.OutsideScopePrefix + "not a resource of CloudFormation stack app (--cfn-stack)"
	if want := map[string]string{"aws_s3_bucket other": outside}; !reflect.DeepEqual(defaults, want) {
		t.Errorf("defaults %v, want %v", defaults, want)
	}
	if got := run.report.CloudFormationStacks; len(got) != 1 || got[0].Resources[0].Matches[0].Scope != scope {
		t.Errorf("stacks %+v", got)
	}
	f := &selection.File{Version: selection.Version, Rules: []selection.Rule{{Include: &selection.Match{Type: selection.Patterns{"*"}}}}}
	selected, _ := run.selectResources(listed, defaults, f, scope, importIDForSelection)
	if got := selectedIDs(selected); !reflect.DeepEqual(got, []string{"app-assets"}) {
		t.Errorf("selected %v", got)
	}
	if want := []report.Excluded{{Type: "aws_s3_bucket", ID: "other", Reason: outside, Scope: scope}}; !reflect.DeepEqual(run.report.Excluded, want) {
		t.Errorf("excluded %+v", run.report.Excluded)
	}
	if report.OwnerOf(outside) != report.OwnerSelection {
		t.Errorf("what the scope leaves out counts as %s", report.OwnerOf(outside))
	}

	// A provider without a scope changes nothing.
	unchanged, err := NewRun().applyImportScope(t.Context(), &fakeProvider{}, &listed, map[string]string{"a b": "c"}, scope)
	if err != nil || !reflect.DeepEqual(unchanged, map[string]string{"a b": "c"}) {
		t.Errorf("unscoped: %v, %v", unchanged, err)
	}
}

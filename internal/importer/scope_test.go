// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
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
	return s, nil
}

func (*stackScopedProvider) CloudFormationStacks() []cfn.Stack {
	return []cfn.Stack{{Name: "app", ID: "stack/app", Resources: []cfn.Resource{
		{LogicalID: "Assets", Type: "AWS::S3::Bucket", Matches: []cfn.Match{{Type: "aws_s3_bucket", ID: "app-assets"}}},
	}}}
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
	defaults := sourcesOf(t, listed, map[string]string{"aws_s3_bucket app-assets": report.OtherToolPrefix + "CloudFormation stack app (per its tag aws:cloudformation:stack-name=app; --include-owned imports it)"})
	run := NewRun()

	if err := run.applyImportScope(t.Context(), &stackScopedProvider{}, listed, defaults, scope); err != nil {
		t.Fatal(err)
	}

	outside := report.OutsideScopePrefix + "not a resource of CloudFormation stack app (--cfn-stack)"
	if want := map[string]string{"aws_s3_bucket other": outside}; !reflect.DeepEqual(reasonsOf(listed, defaults), want) {
		t.Errorf("defaults %v, want %v", reasonsOf(listed, defaults), want)
	}
	if got := run.report.CloudFormationStacks; len(got) != 1 || got[0].Resources[0].Matches[0].Scope != scope {
		t.Errorf("stacks %+v", got)
	}
	f := &selection.File{Version: selection.Version, Rules: []selection.Rule{{Include: &selection.Match{Type: selection.Patterns{"*"}}}}}
	selected, _ := run.selectResources(listed, defaults, f, scope, "", importIDForSelection)
	if got := selectedIDs(selected); !reflect.DeepEqual(got, []string{"app-assets"}) {
		t.Errorf("selected %v", got)
	}
	if want := []report.Excluded{{Type: "aws_s3_bucket", ID: "other", Reason: outside, Scope: scope, Owner: report.OwnerSelection}}; !reflect.DeepEqual(run.report.Excluded, want) {
		t.Errorf("excluded %+v", run.report.Excluded)
	}
	if report.OwnerOf(outside) != report.OwnerSelection {
		t.Errorf("what the scope leaves out counts as %s", report.OwnerOf(outside))
	}

	// A provider without a scope changes nothing.
	unchanged := sourcesOf(t, listed, map[string]string{"aws_s3_bucket other": "c"})
	if err := NewRun().applyImportScope(t.Context(), &fakeProvider{}, listed, unchanged, scope); err != nil || !reflect.DeepEqual(reasonsOf(listed, unchanged), map[string]string{"aws_s3_bucket other": "c"}) {
		t.Errorf("unscoped: %v, %v", reasonsOf(listed, unchanged), err)
	}
}

// stackOnlyProvider is scoped to a stack that has nothing any service
// lists: a custom resource alone.
type stackOnlyProvider struct {
	rootsProvider
}

func (*stackOnlyProvider) ImportScope(context.Context, []harvest.Resource) (*harvest.ImportScope, error) {
	return &harvest.ImportScope{Inside: map[string]bool{}, Outside: map[string]string{}}, nil
}

func (*stackOnlyProvider) CloudFormationStacks() []cfn.Stack {
	return []cfn.Stack{{
		Name: "seeds", ID: "stack/seeds", Region: "eu-west-2", Status: "CREATE_COMPLETE",
		Resources: []cfn.Resource{{LogicalID: "Seed", Type: "Custom::Seed", PhysicalID: "seed", Status: "CREATE_COMPLETE", NotImportable: "a custom resource", Retain: []string{"DeletionPolicy is Delete, the default"}}},
	}}
}

// brokenScopeProvider is scoped to stacks, but can't tell which resources
// are theirs.
type brokenScopeProvider struct {
	rootsProvider
}

func (*brokenScopeProvider) ImportScope(context.Context, []harvest.Resource) (*harvest.ImportScope, error) {
	return nil, errors.New("the stacks can't be matched")
}

// A scope that can't be applied fails the call, rather than leave it to
// import everything listed.
func TestImportScopeFailureStopsTheCall(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	options := pathOptions(out, &fakeEngine{})
	c, err := listCall(t.Context(), options.run(), &brokenScopeProvider{}, options.Options, nil)
	if err == nil || !strings.Contains(err.Error(), "the stacks can't be matched") {
		t.Errorf("call %+v, error %v", c, err)
	}
}

// An import scoped to a stack no service lists anything of lists nothing,
// but still reports the stack, with what keeps it from being safe to
// delete: the run writes its report, and, as nothing failed, succeeds.
func TestImportScopeWithNothingToList(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	options := pathOptions(out, &fakeEngine{})
	run := options.run()
	c, err := listCall(t.Context(), run, &stackOnlyProvider{}, options.Options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.generate(t.Context(), c.byDir); err != nil {
		t.Fatal(err)
	}
	if err := run.finish(nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	r, err := report.Read(out)
	if err != nil {
		t.Fatalf("no report: %v", err)
	}
	if r.ExitCode != report.ExitOK {
		t.Errorf("exit code %d", r.ExitCode)
	}
	if len(r.CloudFormationStacks) != 1 {
		t.Fatalf("stacks %+v", r.CloudFormationStacks)
	}
	s := r.CloudFormationStacks[0]
	if s.SafeToDelete || !strings.Contains(strings.Join(s.Blockers, "\n"), "Seed (Custom::Seed) can't be imported") {
		t.Errorf("stack %+v", s)
	}
}

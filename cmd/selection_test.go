// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/IgnatG/infraharvest/managed"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestCheckSelectionOptions(t *testing.T) {
	for _, tc := range []struct {
		options ImportOptions
		ok      bool
	}{
		{ImportOptions{Selection: "selection.yaml"}, true},
		{ImportOptions{All: true}, true},
		{ImportOptions{Discover: true}, true},
		{ImportOptions{}, false},
		{ImportOptions{Selection: "selection.yaml", All: true}, false},
		{ImportOptions{Discover: true, All: true}, false},
	} {
		if err := checkSelectionOptions(tc.options); (err == nil) != tc.ok {
			t.Errorf("%+v: got %v", tc.options, err)
		}
	}
	if err := checkSelectionOptions(ImportOptions{}); !errors.Is(err, selection.ErrNoSelection) {
		t.Errorf("want ErrNoSelection, got %v", err)
	}
}

var listedForSelection = map[string][]terraformutils.Resource{
	"vpc": {
		terraformutils.NewSimpleResource("vpc-default", "vpc-default", "aws_vpc", "aws", nil),
		terraformutils.NewSimpleResource("vpc-0abc1234", "vpc-0abc1234", "aws_vpc", "aws", nil),
	},
	"s3":          {terraformutils.NewSimpleResource("old-archive", "old-archive", "aws_s3_bucket", "aws", nil)},
	"route_table": {terraformutils.NewSimpleResource("rtbassoc-1", "main", "aws_main_route_table_association", "aws", nil)},
}

var defaultsForSelection = map[string]string{"aws_vpc vpc-default": "default VPC"}

func importIDForSelection(r terraformutils.Resource) (string, bool) {
	return r.InstanceState.ID, r.InstanceInfo.Type != "aws_main_route_table_association"
}

func selectedIDs(selected map[string][]terraformutils.Resource) []string {
	var ids []string
	for _, resources := range selected {
		for _, r := range resources {
			ids = append(ids, r.InstanceState.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestSelectResourcesWithAll(t *testing.T) {
	run := newEngineRun()

	selected, _ := run.selectResources(listedForSelection, defaultsForSelection, nil, "", importIDForSelection)

	// Unimportable resources go on, for importsByDir to count.
	if want := []string{"old-archive", "rtbassoc-1", "vpc-0abc1234"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if want := []report.Excluded{{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"}}; !reflect.DeepEqual(run.report.Excluded, want) {
		t.Errorf("excluded %+v, want %+v", run.report.Excluded, want)
	}
	if run.discovered["aws_vpc"] != 1 {
		t.Errorf("an excluded resource still counts as discovered: %v", run.discovered)
	}
}

func TestSelectResourcesWithFile(t *testing.T) {
	run := newEngineRun()
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		// The file overrides the provider's default.
		{Type: "aws_vpc", ID: "vpc-default", Include: true},
		{Type: "aws_s3_bucket", ID: "old-archive", Include: false, Note: "to be deleted"},
	}}

	selected, _ := run.selectResources(listedForSelection, defaultsForSelection, f, "", importIDForSelection)

	if want := []string{"rtbassoc-1", "vpc-0abc1234", "vpc-default"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if len(run.report.Excluded) != 1 || run.report.Excluded[0].ID != "old-archive" {
		t.Errorf("excluded %+v", run.report.Excluded)
	}
}

// With --accounts, the same type and ID in two accounts are two resources:
// the file's entry for the account being imported decides.
func TestSelectResourcesByScope(t *testing.T) {
	const a, b = "aws/111122223333/global", "aws/444455556666/global"
	listed := map[string][]terraformutils.Resource{
		"iam": {terraformutils.NewSimpleResource("admin", "admin", "aws_iam_role", "aws", nil)},
	}
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: a, Include: true},
		{Type: "aws_iam_role", ID: "admin", Scope: b, Include: false, Note: "the other account's"},
	}}

	run := newEngineRun()
	selected, _ := run.selectResources(listed, nil, f, a, importIDForSelection)
	if !reflect.DeepEqual(selectedIDs(selected), []string{"admin"}) {
		t.Errorf("account %s: selected %v, want the role", a, selectedIDs(selected))
	}

	run = newEngineRun()
	selected, leftOut := run.selectResources(listed, nil, f, b, importIDForSelection)
	if len(selectedIDs(selected)) != 0 || len(leftOut) != 1 {
		t.Errorf("account %s: selected %v, left out %v; want the role excluded", b, selectedIDs(selected), leftOut)
	}
}

// A rule in the file decides an unlisted resource before the provider's
// defaults do, as the file's header says.
func TestSelectResourcesRulesBeforeDefaults(t *testing.T) {
	run := newEngineRun()
	f := &selection.File{
		Version:  selection.Version,
		Defaults: selection.Defaults{Include: true},
		Rules: []selection.Rule{
			{Include: &selection.Match{Type: selection.Patterns{"aws_vpc"}}},
			{Exclude: &selection.Match{Type: selection.Patterns{"aws_s3_bucket"}}},
		},
	}

	selected, _ := run.selectResources(listedForSelection, defaultsForSelection, f, "", importIDForSelection)

	// The default VPC is included by the rule; the bucket excluded by one.
	if want := []string{"rtbassoc-1", "vpc-0abc1234", "vpc-default"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if len(run.report.Excluded) != 1 || run.report.Excluded[0].ID != "old-archive" || run.report.Excluded[0].Reason != "excluded by a rule in the selection file" {
		t.Errorf("excluded %+v", run.report.Excluded)
	}
}

// A resource listed twice, such as by two Import calls of one command,
// gets one entry, and nothing is new in a first file.
func TestDiscoverWritesEachResourceOnce(t *testing.T) {
	run := newEngineRun()
	run.options = ImportOptions{Discover: true, Selection: filepath.Join(t.TempDir(), "selection.yaml")}

	run.addDiscovered(listedForSelection, defaultsForSelection, "aws/123456789012/eu-west-2", importIDForSelection)
	run.addDiscovered(listedForSelection, defaultsForSelection, "aws/123456789012/eu-west-2", importIDForSelection)
	if err := run.writeSelection(); err != nil {
		t.Fatal(err)
	}

	f, err := selection.Load(run.options.Selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Resources) != 3 {
		t.Errorf("want the 3 importable resources once, got %+v", f.Resources)
	}
	for _, r := range f.Resources {
		if r.New {
			t.Errorf("%s %s is marked new in a first file", r.Type, r.ID)
		}
	}
	if d := f.Decide("aws_vpc", "vpc-default", ""); d.Include || d.Reason != "default VPC" {
		t.Errorf("default VPC: %+v", d)
	}
}

func TestDiscoverWritesSelection(t *testing.T) {
	run := newEngineRun()
	run.options = ImportOptions{Discover: true, Selection: filepath.Join(t.TempDir(), "selection.yaml")}

	run.addDiscovered(listedForSelection, defaultsForSelection, "aws/123456789012/eu-west-2", importIDForSelection)
	if err := run.writeSelection(); err != nil {
		t.Fatal(err)
	}

	f, err := selection.Load(run.options.Selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Resources) != 3 {
		t.Errorf("want the 3 importable resources, got %+v", f.Resources)
	}
	if d := f.Decide("aws_vpc", "vpc-default", ""); d.Include || d.Reason != "default VPC" {
		t.Errorf("default VPC: %+v", d)
	}
	if d := f.Decide("aws_vpc", "vpc-0abc1234", ""); !d.Include {
		t.Errorf("own VPC: %+v", d)
	}
	for _, r := range f.Resources {
		if r.Scope != "aws/123456789012/eu-west-2" {
			t.Errorf("%s %s: scope %q", r.Type, r.ID, r.Scope)
		}
	}
}

func TestExcludeManaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	content := `{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "old-archive"}}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := newEngineRun()

	defaults, err := excludeManaged(t.Context(), run, []string{path}, listedForSelection, map[string]string{"aws_vpc vpc-default": "default VPC"}, importIDForSelection)
	if err != nil {
		t.Fatal(err)
	}
	if got := defaults["aws_s3_bucket old-archive"]; got != managed.Reason+" ("+path+")" {
		t.Errorf("managed bucket: %q", got)
	}
	if len(defaults) != 2 {
		t.Errorf("defaults: %v", defaults)
	}
	if _, err := excludeManaged(t.Context(), run, []string{"backend"}, listedForSelection, nil, importIDForSelection); err == nil {
		t.Error("want an error for backend without a configured backend")
	}
}

// Running discover again keeps people's decisions and marks what is new.
func TestDiscoverUpdatesSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	first := newEngineRun()
	first.options = ImportOptions{Discover: true, Selection: path}
	first.addDiscovered(listedForSelection, defaultsForSelection, "", importIDForSelection)
	if err := first.writeSelection(); err != nil {
		t.Fatal(err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Resources {
		if f.Resources[i].ID == "old-archive" {
			f.Resources[i].Include, f.Resources[i].Note = false, "archived"
		}
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	again := newEngineRun()
	again.options = ImportOptions{Discover: true, Selection: path}
	listed := map[string][]terraformutils.Resource{
		"vpc": listedForSelection["vpc"],
		"s3": {
			terraformutils.NewSimpleResource("old-archive", "old-archive", "aws_s3_bucket", "aws", nil),
			terraformutils.NewSimpleResource("new-logs", "new-logs", "aws_s3_bucket", "aws", nil),
		},
	}
	again.addDiscovered(listed, defaultsForSelection, "", importIDForSelection)
	if err := again.writeSelection(); err != nil {
		t.Fatal(err)
	}

	updated, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]selection.Resource{}
	for _, r := range updated.Resources {
		byID[r.ID] = r
	}
	if r := byID["old-archive"]; r.Include || r.Note != "archived" || r.New {
		t.Errorf("kept decision: %+v", r)
	}
	if r := byID["new-logs"]; !r.Include || !r.New {
		t.Errorf("new resource: %+v", r)
	}
	if len(updated.Resources) != 4 {
		t.Errorf("resources: %+v", updated.Resources)
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

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

	selected, _ := run.selectResources(listedForSelection, defaultsForSelection, nil, importIDForSelection)

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

	selected, _ := run.selectResources(listedForSelection, defaultsForSelection, f, importIDForSelection)

	if want := []string{"rtbassoc-1", "vpc-0abc1234", "vpc-default"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if len(run.report.Excluded) != 1 || run.report.Excluded[0].ID != "old-archive" {
		t.Errorf("excluded %+v", run.report.Excluded)
	}
}

func TestDiscoverWritesSelection(t *testing.T) {
	run := newEngineRun()
	run.options = ImportOptions{Discover: true, Selection: filepath.Join(t.TempDir(), "selection.yaml")}

	run.addDiscovered(listedForSelection, defaultsForSelection, importIDForSelection)
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
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

func TestCheckSelectionOptions(t *testing.T) {
	for _, tc := range []struct {
		options Options
		ok      bool
	}{
		{Options{Selection: "selection.yaml"}, true},
		{Options{All: true}, true},
		{Options{Discover: true}, true},
		{Options{}, false},
		{Options{Selection: "selection.yaml", All: true}, false},
		{Options{Discover: true, All: true}, false},
		{Options{All: true, Regenerate: true}, true},
		{Options{All: true, Regenerate: true, Resume: true}, true},
		{Options{All: true, Regenerate: true, Incremental: true}, false},
	} {
		if err := checkSelectionOptions(tc.options); (err == nil) != tc.ok {
			t.Errorf("%+v: got %v", tc.options, err)
		}
	}
	if err := checkSelectionOptions(Options{}); !errors.Is(err, selection.ErrNoSelection) {
		t.Errorf("want ErrNoSelection, got %v", err)
	}
}

var listedForSelection = map[string][]harvest.Resource{
	"vpc": {
		harvest.RecordBare("vpc-default", "vpc-default", "aws_vpc", "aws"),
		harvest.RecordBare("vpc-0abc1234", "vpc-0abc1234", "aws_vpc", "aws"),
	},
	"s3":          {harvest.RecordBare("old-archive", "old-archive", "aws_s3_bucket", "aws")},
	"route_table": {harvest.RecordBare("rtbassoc-1", "main", "aws_main_route_table_association", "aws")},
}

var defaultsForSelection = map[string]string{"aws_vpc vpc-default": "default VPC"}

// defaultsProvider is a provider whose own defaults leave out what
// excluded names, by "<type> <id>".
type defaultsProvider struct {
	fakeProvider
	excluded map[string]string
}

func (p *defaultsProvider) ExcludedByDefault(context.Context, []harvest.Resource) (map[string]string, error) {
	return p.excluded, nil
}

// sourcesOf returns the sources of the default selection of listed, with
// excluded as the provider's own defaults and a catalogue without skip
// rules.
func sourcesOf(t *testing.T, listed listing, excluded map[string]string) *selection.Sources {
	t.Helper()
	sources, err := defaultSelection(t.Context(), &defaultsProvider{excluded: excluded}, &catalogue.Catalogue{}, listed, false)
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

// excludedManaged runs excludeManaged on listedForSelection, with excluded
// as the provider's own defaults, and returns why the default selection
// then leaves out each resource it leaves out, by "<type> <lister ID>".
func excludedManaged(t *testing.T, run *Run, options Options, excluded map[string]string) (map[string]string, error) {
	t.Helper()
	listed := listing{resources: listedForSelection}
	sources := sourcesOf(t, listed, excluded)
	err := excludeManaged(t.Context(), run, options, listed.resources, sources, importIDForSelection)
	return reasonsOf(listed, sources), err
}

// reasonsOf returns why sources leave out each resource of listed they
// leave out, by "<type> <lister ID>".
func reasonsOf(listed listing, sources *selection.Sources) map[string]string {
	reasons := map[string]string{}
	for _, resources := range listed.resources {
		for _, r := range resources {
			if reason := sources.Reason(r); reason != "" {
				reasons[tagKey(r)] = reason
			}
		}
	}
	return reasons
}

func importIDForSelection(r harvest.Resource) (string, bool) {
	return r.Observed.ID, r.Address.Type != "aws_main_route_table_association"
}

func selectedIDs(selected map[string][]harvest.Resource) []string {
	var ids []string
	for _, resources := range selected {
		for _, r := range resources {
			ids = append(ids, r.Observed.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestSelectResourcesWithAll(t *testing.T) {
	run := NewRun()

	selected, _ := run.selectResources(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), nil, "", "", importIDForSelection)

	// Unimportable resources go on, for importsByDir to count.
	if want := []string{"old-archive", "rtbassoc-1", "vpc-0abc1234"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if want := []report.Excluded{{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC", Owner: report.OwnerCloud}}; !reflect.DeepEqual(run.report.Excluded, want) {
		t.Errorf("excluded %+v, want %+v", run.report.Excluded, want)
	}
	if run.discovered["aws_vpc"] != 1 {
		t.Errorf("an excluded resource still counts as discovered: %v", run.discovered)
	}
}

func TestSelectResourcesWithFile(t *testing.T) {
	run := NewRun()
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		// The file overrides the provider's default.
		{Type: "aws_vpc", ID: "vpc-default", Include: true},
		{Type: "aws_s3_bucket", ID: "old-archive", Include: false, Note: "to be deleted"},
	}}

	selected, _ := run.selectResources(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), f, "", "", importIDForSelection)

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
	listed := map[string][]harvest.Resource{
		"iam": {harvest.RecordBare("admin", "admin", "aws_iam_role", "aws")},
	}
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: a, Include: true},
		{Type: "aws_iam_role", ID: "admin", Scope: b, Include: false, Note: "the other account's"},
	}}

	run := NewRun()
	selected, _ := run.selectResources(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, nil), f, a, "", importIDForSelection)
	if !reflect.DeepEqual(selectedIDs(selected), []string{"admin"}) {
		t.Errorf("account %s: selected %v, want the role", a, selectedIDs(selected))
	}

	run = NewRun()
	selected, leftOut := run.selectResources(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, nil), f, b, "", importIDForSelection)
	if len(selectedIDs(selected)) != 0 || len(leftOut) != 1 {
		t.Errorf("account %s: selected %v, left out %v; want the role excluded", b, selectedIDs(selected), leftOut)
	}
}

// A rule in the file decides an unlisted resource before the provider's
// defaults do, as the file's header says.
func TestSelectResourcesRulesBeforeDefaults(t *testing.T) {
	run := NewRun()
	f := &selection.File{
		Version:  selection.Version,
		Defaults: selection.Defaults{Include: true},
		Rules: []selection.Rule{
			{Include: &selection.Match{Type: selection.Patterns{"aws_vpc"}}},
			{Exclude: &selection.Match{Type: selection.Patterns{"aws_s3_bucket"}}},
		},
	}

	selected, _ := run.selectResources(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), f, "", "", importIDForSelection)

	// The default VPC is included by the rule; the bucket excluded by one.
	if want := []string{"rtbassoc-1", "vpc-0abc1234", "vpc-default"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if len(run.report.Excluded) != 1 || run.report.Excluded[0].ID != "old-archive" || run.report.Excluded[0].Reason != "excluded by a rule in the selection file" {
		t.Errorf("excluded %+v", run.report.Excluded)
	}
}

// A rule doesn't include an unlisted resource the defaults leave out
// because another tool manages it: only its entry or --include-owned does.
// An exclude rule still excludes it.
func TestSelectResourcesRulesKeepOwnedOut(t *testing.T) {
	const stack = "managed by CloudFormation stack app"
	listed := map[string][]harvest.Resource{"s3": {
		harvest.RecordBare("stack-bucket", "stack-bucket", "aws_s3_bucket", "aws"),
		harvest.RecordBare("kept-bucket", "kept-bucket", "aws_s3_bucket", "aws"),
		harvest.RecordBare("app-bucket", "app-bucket", "aws_s3_bucket", "aws"),
	}}
	defaults := map[string]string{"aws_s3_bucket stack-bucket": stack, "aws_s3_bucket kept-bucket": stack}
	file := func(rule selection.Rule, includeOwned bool) *selection.File {
		return &selection.File{
			Version:      selection.Version,
			Rules:        []selection.Rule{rule},
			Resources:    []selection.Resource{{Type: "aws_s3_bucket", ID: "kept-bucket", Include: true}},
			IncludeOwned: includeOwned,
		}
	}
	include := selection.Rule{Include: &selection.Match{Type: selection.Patterns{"aws_s3_bucket"}}}
	exclude := selection.Rule{Exclude: &selection.Match{Type: selection.Patterns{"aws_s3_bucket"}}}

	run := NewRun()
	selected, _ := run.selectResources(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, defaults), file(include, false), "", "", importIDForSelection)
	if want := []string{"app-bucket", "kept-bucket"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("selected %v, want %v", selectedIDs(selected), want)
	}
	if want := []report.Excluded{{Type: "aws_s3_bucket", ID: "stack-bucket", Reason: stack, Owner: report.OwnerCloudFormation}}; !reflect.DeepEqual(run.report.Excluded, want) {
		t.Errorf("excluded %+v, want %+v", run.report.Excluded, want)
	}

	run = NewRun()
	selected, _ = run.selectResources(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, defaults), file(include, true), "", "", importIDForSelection)
	if want := []string{"app-bucket", "kept-bucket", "stack-bucket"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("with --include-owned: selected %v, want %v", selectedIDs(selected), want)
	}

	run = NewRun()
	selected, _ = run.selectResources(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, defaults), file(exclude, false), "", "", importIDForSelection)
	if want := []string{"kept-bucket"}; !reflect.DeepEqual(selectedIDs(selected), want) {
		t.Errorf("with an exclude rule: selected %v, want %v", selectedIDs(selected), want)
	}
}

// --include-owned lets the selection file's rules include what another
// tool manages.
func TestSelectionFileIncludeOwned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte("version: 1\ndefaults:\n  include: true\nresources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := NewRun().selectionFile(Options{Selection: path, IncludeOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	if !f.IncludeOwned {
		t.Error("the file's rules may not include what another tool manages")
	}
}

// A resource listed twice, such as by two Import calls of one command,
// gets one entry, and nothing is new in a first file.
func TestDiscoverWritesEachResourceOnce(t *testing.T) {
	run := NewRun()
	run.options = Options{Discover: true, Selection: filepath.Join(t.TempDir(), "selection.yaml")}

	run.addDiscovered(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), "aws/123456789012/eu-west-2", importIDForSelection)
	run.addDiscovered(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), "aws/123456789012/eu-west-2", importIDForSelection)
	if err := run.writeSelection(false); err != nil {
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
	run := NewRun()
	run.options = Options{Discover: true, Selection: filepath.Join(t.TempDir(), "selection.yaml")}

	run.addDiscovered(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), "aws/123456789012/eu-west-2", importIDForSelection)
	if err := run.writeSelection(false); err != nil {
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
	run := NewRun()

	defaults, err := excludedManaged(t, run, Options{ManagedState: []string{path}}, map[string]string{"aws_vpc vpc-default": "default VPC"})
	if err != nil {
		t.Fatal(err)
	}
	if got := defaults["aws_s3_bucket old-archive"]; got != managed.Reason+" ("+path+")" {
		t.Errorf("managed bucket: %q", got)
	}
	if len(defaults) != 2 {
		t.Errorf("defaults: %v", defaults)
	}
	if _, err := excludedManaged(t, run, Options{ManagedState: []string{"backend"}}, nil); err == nil {
		t.Error("want an error for backend without a configured backend")
	}
}

// With --regenerate, what a root an earlier import generated has isn't
// left out as managed, even though state has it: generated again without
// it, the root would lose its block, and a plan destroy it. A root whose
// checkpoint can't be read fails the import instead.
func TestExcludeManagedKeepsWhatRootsHave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	content := `{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "old-archive"}}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123456789012", "eu-west-2")
	writeRoot(t, dir, engine.VersionsFileName, "storage.tf")
	result := &engine.Result{Addresses: []engine.Managed{{Type: "aws_s3_bucket", ID: "old-archive", Address: "aws_s3_bucket.old_archive"}}}
	if err := writeCheckpoint(out, dir, checkpoint{Result: result}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		options Options
		managed bool
	}{
		"regenerate":  {Options{ManagedState: []string{path}, Out: out, Regenerate: true}, false},
		"resume":      {Options{ManagedState: []string{path}, Out: out, Resume: true}, false},
		"incremental": {Options{ManagedState: []string{path}, Out: out, Incremental: true}, true},
		"a new root":  {Options{ManagedState: []string{path}, Out: t.TempDir(), Regenerate: true}, true},
	} {
		defaults, err := excludedManaged(t, NewRun(), tc.options, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, got := defaults["aws_s3_bucket old-archive"]; got != tc.managed {
			t.Errorf("%s: left out as managed %v, want %v", name, got, tc.managed)
		}
	}

	if err := os.WriteFile(checkpointPath(out, dir), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := excludedManaged(t, NewRun(), Options{ManagedState: []string{path}, Out: out, Regenerate: true}, nil); err == nil || !strings.Contains(err.Error(), "aws/123456789012/eu-west-2") {
		t.Errorf("an unreadable checkpoint: %v", err)
	}
}

// With --regenerate, what a root's own state has stays the root's, at the
// address the state has it, in place of one only inferred, and with what
// the root's checkpoint doesn't say; what another state has is left out
// as managed.
func TestExcludeManagedReadsRootsOwnState(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123456789012", "eu-west-2")
	writeRoot(t, dir, engine.VersionsFileName, "storage.tf")
	// From an older release, with imports.tf deleted: the bucket's address
	// is only inferred, as the call it was moved into.
	older := &engine.Result{
		Imported: []engine.Import{{Type: "aws_s3_bucket", Name: "old_archive", ID: "old-archive"}},
		Modules:  []engine.ModuleCall{{Name: "archive", Resources: []string{"aws_s3_bucket.old_archive"}}},
	}
	if err := writeCheckpoint(out, dir, checkpoint{Result: older}); err != nil {
		t.Fatal(err)
	}
	own := `{"version": 4, "resources": [
	  {"mode": "managed", "module": "module.archive", "type": "aws_s3_bucket", "name": "this", "instances": [{"index_key": 0, "attributes": {"id": "old-archive"}}]},
	  {"mode": "managed", "type": "aws_vpc", "name": "main", "instances": [{"attributes": {"id": "vpc-0abc1234"}}]}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "network.tfstate")
	if err := os.WriteFile(other, []byte(`{"version": 4, "resources": [{"mode": "managed", "type": "aws_vpc", "name": "default", "instances": [{"attributes": {"id": "vpc-default"}}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := NewRun()

	defaults, err := excludedManaged(t, run, Options{ManagedState: []string{out, other}, Out: out, Regenerate: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"aws_vpc vpc-default": managed.Reason + " (" + other + ")"}; !reflect.DeepEqual(defaults, want) {
		t.Errorf("defaults: %v, want %v", defaults, want)
	}
	got, err := run.shared.previousAddressesOf(out, dir)
	want := []engine.Managed{
		{Type: "aws_s3_bucket", ID: "old-archive", Address: "module.archive.aws_s3_bucket.this[0]"},
		{Type: "aws_vpc", ID: "vpc-0abc1234", Address: "aws_vpc.main"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("previous addresses: %+v, %v, want %+v", got, err, want)
	}
	// Without the state, the address stays inferred.
	if got, _ := NewRun().shared.previousAddressesOf(out, dir); len(got) != 1 || !got[0].Unverified {
		t.Errorf("without the state: %+v", got)
	}
}

// A root's own state is its local terraform.tfstate, and where the
// configured backend keeps it for a root with backend.tf.
func TestOwnStates(t *testing.T) {
	out := t.TempDir()
	withBackend := filepath.Join(out, "aws", "1", "eu-west-2")
	local := filepath.Join(out, "aws", "1", "global")
	writeRoot(t, withBackend, BackendFileName)
	writeRoot(t, local)
	backend := &config.Backend{S3: &config.S3{Bucket: "acme-state", Region: "eu-west-2", KeyPrefix: "imported"}}

	own := ownStates(out, []string{withBackend, local}, backend)
	want := map[string]string{
		stateLocation(filepath.Join(withBackend, "terraform.tfstate")): withBackend,
		stateLocation(filepath.Join(local, "terraform.tfstate")):       local,
		"s3://acme-state/imported/aws/1/eu-west-2/terraform.tfstate":   withBackend,
	}
	if !reflect.DeepEqual(own, want) {
		t.Errorf("got %v, want %v", own, want)
	}
}

// What a root's own state says replaces only the addresses previous
// infers: those it records are where the root's configuration has them,
// which moved blocks of its moves.tf may not have moved in state yet.
func TestWithStated(t *testing.T) {
	t.Parallel()
	previous := []engine.Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs", Unverified: true},
		{Type: "aws_sqs_queue", ID: "jobs", Address: "aws_sqs_queue.jobs"},
	}
	stated := []engine.Managed{
		{Type: "aws_sqs_queue", ID: "jobs", Address: "aws_sqs_queue.old_jobs"},
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this[0]"},
		{Type: "aws_iam_role", ID: "app", Address: "aws_iam_role.app"},
	}
	want := []engine.Managed{
		{Type: "aws_iam_role", ID: "app", Address: "aws_iam_role.app"},
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this[0]"},
		{Type: "aws_sqs_queue", ID: "jobs", Address: "aws_sqs_queue.jobs"},
	}
	if got := withStated(previous, stated); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := withStated(previous, nil); !reflect.DeepEqual(got, previous) {
		t.Errorf("without state: %+v", got)
	}
}

func TestWithProfile(t *testing.T) {
	for _, tc := range []struct{ source, profile, want string }{
		{"s3://state/roots/?region=eu-west-2", "prod", "s3://state/roots/?profile=prod&region=eu-west-2"},
		{"s3://state/roots/?profile=state", "prod", "s3://state/roots/?profile=state"},
		{"s3://state/roots/", "default", "s3://state/roots/"},
		{"s3://state/roots/", "", "s3://state/roots/"},
		{"gs://state/roots/", "prod", "gs://state/roots/"},
		{"terraform.tfstate", "prod", "terraform.tfstate"},
	} {
		if got := withProfile(tc.source, tc.profile); got != tc.want {
			t.Errorf("withProfile(%q, %q) = %q, want %q", tc.source, tc.profile, got, tc.want)
		}
	}
}

// Running discover again keeps people's decisions and marks what is new.
func TestDiscoverUpdatesSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	first := NewRun()
	first.options = Options{Discover: true, Selection: path}
	first.addDiscovered(listing{resources: listedForSelection}, sourcesOf(t, listing{resources: listedForSelection}, defaultsForSelection), "", importIDForSelection)
	if err := first.writeSelection(false); err != nil {
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

	again := NewRun()
	again.options = Options{Discover: true, Selection: path}
	listed := map[string][]harvest.Resource{
		"vpc": listedForSelection["vpc"],
		"s3": {
			harvest.RecordBare("old-archive", "old-archive", "aws_s3_bucket", "aws"),
			harvest.RecordBare("new-logs", "new-logs", "aws_s3_bucket", "aws"),
		},
	}
	again.addDiscovered(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, defaultsForSelection), "", importIDForSelection)
	if err := again.writeSelection(false); err != nil {
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

// Running discover again over one region, or one service, keeps the
// decisions of the entries elsewhere: it drops only what it no longer
// finds where it listed.
func TestDiscoverOfLessKeepsTheRest(t *testing.T) {
	const a, b = "aws/123456789012/eu-west-1", "aws/123456789012/eu-west-2"
	path := filepath.Join(t.TempDir(), "selection.yaml")
	importID := func(r harvest.Resource) (string, bool) { return r.Observed.ID, true }
	services := Options{Discover: true, Selection: path, Services: []string{"s3", "vpc"}}

	first := NewRun()
	first.options = services
	for _, scope := range []string{a, b} {
		listed := map[string][]harvest.Resource{
			"vpc": {harvest.RecordBare("vpc-"+scope[len(scope)-1:], "vpc", "aws_vpc", "aws")},
			"s3":  {harvest.RecordBare("bucket-"+scope[len(scope)-1:], "bucket", "aws_s3_bucket", "aws")},
		}
		first.addDiscovered(listing{resources: listed}, sourcesOf(t, listing{resources: listed}, nil), scope, importID)
		first.cover(scope, services)
	}
	if err := first.writeSelection(false); err != nil {
		t.Fatal(err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Resources {
		f.Resources[i].Include, f.Resources[i].Note = false, "reviewed"
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	// Region b again, with its VPC only: its bucket is gone, and the rest
	// wasn't listed.
	again := NewRun()
	vpcOnly := Options{Discover: true, Selection: path, Services: []string{"vpc"}}
	again.options = vpcOnly
	again.addDiscovered(listing{resources: map[string][]harvest.Resource{"vpc": nil}}, sourcesOf(t, listing{resources: map[string][]harvest.Resource{"vpc": nil}}, nil), b, importID)
	again.cover(b, vpcOnly)
	if err := again.writeSelection(false); err != nil {
		t.Fatal(err)
	}
	updated, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.HasIn(b, "aws_vpc", "vpc-2") {
		t.Errorf("region %s's VPC, no longer found, is still listed", b)
	}
	for _, kept := range []struct{ scope, typ, id string }{
		{a, "aws_vpc", "vpc-1"},
		{a, "aws_s3_bucket", "bucket-1"},
		{b, "aws_s3_bucket", "bucket-2"},
	} {
		if !updated.HasIn(kept.scope, kept.typ, kept.id) {
			t.Errorf("%s %s %s was dropped", kept.scope, kept.typ, kept.id)
			continue
		}
		if d := updated.DecideIn(kept.scope, kept.typ, kept.id, "", nil); d.Include {
			t.Errorf("%s %s %s lost its decision: %+v", kept.scope, kept.typ, kept.id, d)
		}
	}

	// With a filter, discover can't tell what it didn't match from what is
	// gone, so nothing is dropped.
	filtered := NewRun()
	withTag := Options{Discover: true, Selection: path, Services: []string{"s3", "vpc"}, TagFilters: []string{"env=prod"}}
	filtered.options = withTag
	filtered.addDiscovered(listing{resources: map[string][]harvest.Resource{"vpc": nil, "s3": nil}}, sourcesOf(t, listing{resources: map[string][]harvest.Resource{"vpc": nil, "s3": nil}}, nil), a, importID)
	filtered.cover(a, withTag)
	if err := filtered.writeSelection(false); err != nil {
		t.Fatal(err)
	}
	if updated, err = selection.Load(path); err != nil {
		t.Fatal(err)
	}
	if len(updated.Resources) != 3 {
		t.Errorf("a filtered discover dropped entries: %+v", updated.Resources)
	}
}

// The state --managed-state names is read once per command: the other
// Import calls, and the accounts imported alongside, reuse it.
func TestExcludeManagedReadsStateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	content := `{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "old-archive"}}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := NewRun()
	options := Options{ManagedState: []string{path}}
	if _, err := excludedManaged(t, run, options, nil); err != nil {
		t.Fatal(err)
	}
	// Gone now: a second read would fail.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Run{run, run.child()} {
		defaults, err := excludedManaged(t, r, options, nil)
		if err != nil {
			t.Fatalf("the state was read again: %v", err)
		}
		if _, ok := defaults["aws_s3_bucket old-archive"]; !ok {
			t.Errorf("defaults: %v", defaults)
		}
	}
}

// discover --format json prints what it listed, by what manages it, on the
// command's output, rather than opening the picker.
func TestDiscoverPrintsSummary(t *testing.T) {
	var out bytes.Buffer
	run := NewRun()
	run.used = true
	run.OpenPicker = true
	run.options = Options{Discover: true, Selection: filepath.Join(t.TempDir(), "selection.yaml"), Format: FormatJSON, Stdout: &out, ReportTags: []string{"team"}}
	const scope = "aws/1/eu-west-2"
	stack := report.OtherToolPrefix + "CloudFormation stack app"
	run.listed = append(run.listed, []selection.Resource{
		{Type: "aws_s3_bucket", ID: "logs", Scope: scope, Include: true, Tags: map[string]string{"team": "web"}},
		{Type: "aws_s3_bucket", ID: "logs", Scope: scope, Include: true, Tags: map[string]string{"team": "web"}},
		{Type: "aws_s3_bucket", ID: "state", Scope: scope, Reason: managed.Reason + " (prod.tfstate)"},
		{Type: "aws_s3_bucket", ID: "assets", Scope: scope, Reason: stack},
		{Type: "aws_vpc", ID: "vpc-default", Scope: scope, Reason: "default VPC"},
	}...)

	if err := run.finish(nil); err != nil {
		t.Fatal(err)
	}

	var got report.Discovery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	want := report.DiscoveryCount{Listed: 4, Included: 1, Excluded: 3, TerraformState: 1, CloudFormation: 1, Cloud: 1}
	if got.DiscoveryCount != want || got.Selection != run.options.Selection || len(got.Types) != 2 || len(got.Scopes) != 1 || len(got.Tags) != 2 {
		t.Errorf("summary: %s", out.String())
	}
	d := run.summary()
	if lines := tagLines(d); !reflect.DeepEqual(lines, []string{"by team: (not tagged) 0 of 3 not managed, web 1 of 1 not managed"}) {
		t.Errorf("tag lines: %q", lines)
	}
}

// Running discover again, with --managed-state, finds that a bucket has
// left the state: its entry is included again, for review. After a listing
// that failed in part, it stays as it was.
func TestDiscoverRefreshesManaged(t *testing.T) {
	for _, partial := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "selection.yaml")
		reason := managed.Reason + " (prod.tfstate)"
		first := NewRun()
		first.options = Options{Discover: true, Selection: path, ManagedState: []string{"prod.tfstate"}}
		first.listed = []selection.Resource{{Type: "aws_s3_bucket", ID: "logs", Service: "s3", Reason: reason}}
		if err := first.writeSelection(false); err != nil {
			t.Fatal(err)
		}

		again := NewRun()
		again.options = first.options
		again.listed = []selection.Resource{{Type: "aws_s3_bucket", ID: "logs", Service: "s3", Include: true}}
		if err := again.writeSelection(partial); err != nil {
			t.Fatal(err)
		}

		f, err := selection.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		r := f.Resources[0]
		if partial && (r.Include || r.Reason != reason) {
			t.Errorf("after a partial listing: %+v", r)
		}
		if !partial && (!r.Include || r.Reason != "" || !r.New) {
			t.Errorf("want the bucket included again, for review: %+v", r)
		}
	}
}

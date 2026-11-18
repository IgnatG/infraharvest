// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// fakeState is a StateTerraform that records its commands. Its plans
// write a change summary with imports imports, and show plan.
type fakeState struct {
	Terraform
	imports int
	plan    *tfjson.Plan
	planErr error

	inits, applies int
	planOpts       int
	commands       []string
	failApply      error
	failState      error
}

func (f *fakeState) Init(context.Context, ...tfexec.InitOption) error {
	f.inits++
	return nil
}

func (f *fakeState) PlanJSON(_ context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	f.planOpts = len(opts)
	fmt.Fprintf(w, `{"@level":"info","@message":"Plan","type":"change_summary","changes":{"add":0,"change":1,"import":%d,"remove":0,"operation":"plan"}}`+"\n", f.imports)
	return true, f.planErr
}

func (f *fakeState) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	return f.plan, nil
}

func (f *fakeState) SetStdout(io.Writer) {}

func (f *fakeState) Apply(context.Context, ...tfexec.ApplyOption) error {
	f.applies++
	return f.failApply
}

func (f *fakeState) StateMv(_ context.Context, from, to string, _ ...tfexec.StateMvCmdOption) error {
	f.commands = append(f.commands, "mv "+from+" "+to)
	return f.failState
}

func (f *fakeState) StateRm(_ context.Context, address string, _ ...tfexec.StateRmCmdOption) error {
	f.commands = append(f.commands, "rm "+address)
	return f.failState
}

func importPlan(addresses ...string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, a := range addresses {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{
			Address: a, Mode: tfjson.ManagedResourceMode,
			Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Importing: &tfjson.Importing{ID: "x"}},
		})
	}
	return p
}

func TestCheckImportOnly(t *testing.T) {
	t.Parallel()
	update := importPlan("aws_s3_bucket.a")
	update.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
	create := importPlan("aws_s3_bucket.a")
	create.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionCreate}
	notImporting := importPlan("aws_s3_bucket.a")
	notImporting.ResourceChanges[0].Change.Importing = nil
	invokes := importPlan("aws_s3_bucket.a")
	invokes.ActionInvocations = []*tfjson.ActionInvocation{{}}
	deferred := importPlan("aws_s3_bucket.a")
	deferred.DeferredChanges = []*tfjson.DeferredResourceChange{{}}
	dataWrites := importPlan("aws_s3_bucket.a")
	dataWrites.ResourceChanges = append(dataWrites.ResourceChanges, &tfjson.ResourceChange{
		Address: "data.aws_caller_identity.me", Mode: tfjson.DataResourceMode,
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate}},
	})
	dataReads := importPlan("aws_s3_bucket.a")
	dataReads.ResourceChanges = append(dataReads.ResourceChanges, &tfjson.ResourceChange{
		Address: "data.aws_caller_identity.me", Mode: tfjson.DataResourceMode,
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionRead}},
	})

	for name, tc := range map[string]struct {
		plan *tfjson.Plan
		ok   bool
	}{
		"imports only":       {importPlan("aws_s3_bucket.a", "aws_s3_bucket.b"), true},
		"reads data sources": {dataReads, true},
		"no plan":            {nil, false},
		"an update":          {update, false},
		"a create":           {create, false},
		"not an import":      {notImporting, false},
		"an action":          {invokes, false},
		"a deferred change":  {deferred, false},
		"a data write":       {dataWrites, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := CheckImportOnly(tc.plan); (err == nil) != tc.ok {
				t.Errorf("got %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestSnapshotOverride(t *testing.T) {
	t.Parallel()
	got := string(SnapshotOverride([]string{"aws_s3_bucket.a", "aws_iam_role.b", "bad"}, map[string][]string{"aws_iam_role.b": {"name", "inline_policy"}}))
	for _, want := range []string{`resource "aws_s3_bucket" "a" {`, `resource "aws_iam_role" "b" {`, "ignore_changes = all", "ignore_changes = [name, inline_policy]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "bad") {
		t.Errorf("a non-address was written:\n%s", got)
	}
}

func TestBuildSnapshot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // snapshots go to the system's temporary directory
	build := func(plan *tfjson.Plan, imports int) (*fakeState, *Snapshot, string, error) {
		dir := t.TempDir()
		tf := &fakeState{imports: imports, plan: plan}
		s, err := BuildSnapshot(context.Background(), SnapshotBuild{
			Plan: tf, Apply: tf, Dir: dir,
			Resources: []string{"aws_s3_bucket.a"},
			Imports:   []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}},
		})
		return tf, s, dir, err
	}

	tf, s, dir, err := build(importPlan("aws_s3_bucket.a"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if tf.inits != 1 || tf.applies != 1 || s.Len() != 1 {
		t.Errorf("inits %d, applies %d, entries %d", tf.inits, tf.applies, s.Len())
	}
	override, err := os.ReadFile(filepath.Join(dir, SnapshotOverrideFile))
	if err != nil || !strings.Contains(string(override), "ignore_changes = all") {
		t.Errorf("override: %q, %v", override, err)
	}
	if info, err := os.Stat(filepath.Dir(s.Path())); err != nil || info.Mode().Perm() != 0o700 && runtime.GOOS != "windows" {
		t.Errorf("the snapshot directory is not private: %v, %v", info, err)
	}
	if !strings.Contains(filepath.Base(filepath.Dir(s.Path())), SnapshotPattern) {
		t.Errorf("the snapshot is in %s", s.Path())
	}
	s.Remove()
	if _, err := os.Stat(filepath.Dir(s.Path())); !os.IsNotExist(err) {
		t.Errorf("Remove leaves the directory: %v", err)
	}

	// A plan that does more than import is never applied.
	update := importPlan("aws_s3_bucket.a")
	update.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
	tf, _, _, err = build(update, 1)
	if err == nil || tf.applies != 0 {
		t.Errorf("an update plan: error %v, applies %d", err, tf.applies)
	}
	// Neither is one that doesn't import every import block.
	tf, _, _, err = build(importPlan(), 0)
	if err == nil || tf.applies != 0 {
		t.Errorf("a plan without the imports: error %v, applies %d", err, tf.applies)
	}
	// A failed apply leaves nothing behind.
	failing := &fakeState{imports: 1, plan: importPlan("aws_s3_bucket.a"), failApply: errors.New("boom")}
	if _, err := BuildSnapshot(context.Background(), SnapshotBuild{Plan: failing, Apply: failing, Dir: t.TempDir(), Imports: []ImportBlock{{To: "aws_s3_bucket.a"}}}); err == nil {
		t.Error("a failed apply built a snapshot")
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), SnapshotPattern+"*"))
	if len(left) != 0 {
		t.Errorf("snapshots left behind: %v", left)
	}
}

// Prepare edits the copy once it is initialised, and before it is planned;
// if it fails, nothing is planned or applied.
func TestBuildSnapshotPreparesTheCopy(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var order []string
	tf := &orderedState{fakeState: fakeState{imports: 1, plan: importPlan("aws_s3_bucket.a")}, order: &order}
	build := SnapshotBuild{
		Plan: tf, Apply: tf, Dir: t.TempDir(),
		Imports: []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}},
		Prepare: func(_ context.Context, got Terraform) error {
			if got != tf {
				t.Errorf("prepared with another runner: %v", got)
			}
			order = append(order, "prepare")
			return nil
		},
	}
	s, err := BuildSnapshot(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	s.Remove()
	if want := "init,prepare,plan,apply"; strings.Join(order, ",") != want {
		t.Errorf("order: got %v, want %s", order, want)
	}

	order = nil
	build.Prepare = func(context.Context, Terraform) error { return errors.New("boom") }
	if _, err := BuildSnapshot(context.Background(), build); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failed prepare: %v", err)
	}
	if want := "init"; strings.Join(order, ",") != want || tf.applies != 1 {
		t.Errorf("after a failed prepare: commands %v, applies %d", order, tf.applies)
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), SnapshotPattern+"*"))
	if len(left) != 0 {
		t.Errorf("snapshots left behind: %v", left)
	}
}

// orderedState records the order of the commands that build a snapshot.
type orderedState struct {
	fakeState
	order *[]string
}

func (o *orderedState) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	*o.order = append(*o.order, "init")
	return o.fakeState.Init(ctx, opts...)
}

func (o *orderedState) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	*o.order = append(*o.order, "plan")
	return o.fakeState.PlanJSON(ctx, w, opts...)
}

func (o *orderedState) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	*o.order = append(*o.order, "apply")
	return o.fakeState.Apply(ctx, opts...)
}

func TestRemoveStaleSnapshots(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	old, young, other := filepath.Join(tmp, SnapshotPattern+"old"), filepath.Join(tmp, SnapshotPattern+"young"), filepath.Join(tmp, "other-old")
	oldPlan, oldState := filepath.Join(tmp, PlanPattern+"old"), filepath.Join(tmp, EmptyStatePattern+"old")
	oldDrift, oldRoots, oldMCP := filepath.Join(tmp, DriftPattern+"old"), filepath.Join(tmp, DriftRootsPattern+"old"), filepath.Join(tmp, MCPPattern+"old")
	youngRoots, youngMCP := filepath.Join(tmp, DriftRootsPattern+"young"), filepath.Join(tmp, MCPPattern+"young")
	for _, d := range []string{old, young, other, oldPlan, oldState, oldDrift, oldRoots, oldMCP, youngRoots, youngMCP} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{old, other, oldPlan, oldState, oldDrift, oldRoots, oldMCP} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	removeStaleSnapshots(tmp, time.Now(), staleSnapshotAge)
	// The drift roots' prefix overlaps DriftPattern: old ones go, a running
	// process's young ones stay.
	for dir, want := range map[string]bool{
		old: false, young: true, other: true, oldPlan: false, oldState: false,
		oldDrift: false, oldRoots: false, oldMCP: false, youngRoots: true, youngMCP: true,
	} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
}

// validatingState fails the plan while the override in dir has rejected
// ignore all changes, as a provider's plan check does when it is handed
// the imported values as the configuration.
type validatingState struct {
	fakeState
	dir, rejected string
	plans         int
}

func (v *validatingState) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	v.plans++
	override, err := os.ReadFile(filepath.Join(v.dir, SnapshotOverrideFile))
	if err != nil {
		return false, err
	}
	typ, name, _ := strings.Cut(v.rejected, ".")
	if strings.Contains(string(override), fmt.Sprintf("%q %q {\n  lifecycle {\n    ignore_changes = all", typ, name)) {
		fmt.Fprintln(w, `{"@level":"error","@message":"Error: storage_iops","type":"diagnostic","diagnostic":{"severity":"error","summary":"storage_iops is only supported when storage_type is PremiumV2_LRS","detail":""}}`)
		return false, nil
	}
	return v.fakeState.PlanJSON(ctx, w, opts...)
}

func TestBuildSnapshotIgnoresOnlyWhatTheConfigurationSetsWhereAllFails(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	tf := &validatingState{fakeState: fakeState{imports: 2, plan: importPlan("aws_s3_bucket.a", "azurerm_postgresql_flexible_server.db")}, dir: dir, rejected: "azurerm_postgresql_flexible_server.db"}
	build := SnapshotBuild{
		Plan: tf, Apply: tf, Dir: dir,
		Resources: []string{"aws_s3_bucket.a", "azurerm_postgresql_flexible_server.db"},
		Imports:   []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}, {To: "azurerm_postgresql_flexible_server.db", ID: "db"}},
		Blame: func([]tfjson.Diagnostic) map[string][]string {
			return map[string][]string{"azurerm_postgresql_flexible_server.db": {"name", "sku_name"}}
		},
	}
	s, err := BuildSnapshot(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if tf.plans != 2 || tf.applies != 1 {
		t.Errorf("plans %d, applies %d: want 2 and 1", tf.plans, tf.applies)
	}
	override, _ := os.ReadFile(filepath.Join(dir, SnapshotOverrideFile))
	want := `resource "aws_s3_bucket" "a" {
  lifecycle {
    ignore_changes = all
  }
}

resource "azurerm_postgresql_flexible_server" "db" {
  lifecycle {
    ignore_changes = [name, sku_name]
  }
}

`
	if string(override) != want {
		t.Errorf("override:\n%s\nwant:\n%s", override, want)
	}

	// Errors it can't blame on the override still fail the build, after
	// one plan.
	tf.plans = 0
	build.Blame = func([]tfjson.Diagnostic) map[string][]string { return nil }
	if _, err := BuildSnapshot(context.Background(), build); err == nil || !strings.Contains(err.Error(), "doesn't plan") {
		t.Errorf("unblamed errors: %v", err)
	}
	if tf.plans != 1 {
		t.Errorf("plans for unblamed errors: %d", tf.plans)
	}
}

func TestCheckImportOnlyNamesWhatAnUpdateChanges(t *testing.T) {
	t.Parallel()
	p := importPlan("aws_sns_topic_subscription.main")
	c := p.ResourceChanges[0].Change
	c.Actions = tfjson.Actions{tfjson.ActionUpdate}
	c.Before = map[string]any{"endpoint": "e", "confirmation_timeout_in_minutes": nil, "arn": "a"}
	c.After = map[string]any{"endpoint": "e", "confirmation_timeout_in_minutes": float64(1), "arn": nil}
	c.AfterUnknown = map[string]any{"arn": true}
	err := CheckImportOnly(p)
	if err == nil || !strings.HasSuffix(err.Error(), "(update of confirmation_timeout_in_minutes)") {
		t.Errorf("got %v", err)
	}
}

// defaultingState plans the update of the arguments a provider defaults
// (null in what the import read, set by the plan) for as long as the
// override in dir doesn't ignore them, and the import alone after. Its
// plans fail while the override has the resource ignore all changes.
type defaultingState struct {
	fakeState
	dir, resource string
	defaults      map[string]any // argument to the default
	plans         int
}

func (d *defaultingState) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	d.plans++
	override, err := os.ReadFile(filepath.Join(d.dir, SnapshotOverrideFile))
	if err != nil {
		return false, err
	}
	if strings.Contains(string(override), "ignore_changes = all") {
		fmt.Fprintln(w, `{"@level":"error","@message":"Error: filter_policy","type":"diagnostic","diagnostic":{"severity":"error","summary":"filter_policy is required when filter_policy_scope is set","detail":""}}`)
		return false, nil
	}
	return d.fakeState.PlanJSON(ctx, w, opts...)
}

func (d *defaultingState) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	override, err := os.ReadFile(filepath.Join(d.dir, SnapshotOverrideFile))
	if err != nil {
		return nil, err
	}
	p := importPlan(d.resource)
	typ, name, _ := strings.Cut(d.resource, ".")
	rc := p.ResourceChanges[0]
	rc.Type, rc.Name = typ, name
	before, after := map[string]any{"endpoint": "e"}, map[string]any{"endpoint": "e"}
	changed := false
	for argument, value := range d.defaults {
		if strings.Contains(string(override), argument) {
			continue
		}
		before[argument], after[argument] = nil, value
		changed = true
	}
	if changed {
		rc.Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
		rc.Change.Before, rc.Change.After = before, after
	}
	return p, nil
}

func TestBuildSnapshotIgnoresWhatTheProviderDefaults(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	tf := &defaultingState{
		fakeState: fakeState{imports: 1},
		dir:       dir, resource: "aws_sns_topic_subscription.main",
		defaults: map[string]any{"confirmation_timeout_in_minutes": float64(1), "endpoint_auto_confirms": false},
	}
	build := SnapshotBuild{
		Plan: tf, Apply: tf, Dir: dir,
		Resources: []string{"aws_sns_topic_subscription.main"},
		Imports:   []ImportBlock{{To: "aws_sns_topic_subscription.main", ID: "arn"}},
		Blame: func([]tfjson.Diagnostic) map[string][]string {
			return map[string][]string{"aws_sns_topic_subscription.main": {"endpoint", "protocol", "topic_arn"}}
		},
	}
	s, err := BuildSnapshot(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if tf.plans != 3 || tf.applies != 1 {
		t.Errorf("plans %d, applies %d: want 3 and 1", tf.plans, tf.applies)
	}
	override, _ := os.ReadFile(filepath.Join(dir, SnapshotOverrideFile))
	want := `resource "aws_sns_topic_subscription" "main" {
  lifecycle {
    ignore_changes = [confirmation_timeout_in_minutes, endpoint, endpoint_auto_confirms, protocol, topic_arn]
  }
}

`
	if string(override) != want {
		t.Errorf("override:\n%s\nwant:\n%s", override, want)
	}
}

func TestProviderDefaults(t *testing.T) {
	t.Parallel()
	partial := map[string][]string{"aws_sns_topic_subscription.main": {"endpoint"}}
	update := func(address, typ, name string, before, after map[string]any) *tfjson.Plan {
		p := importPlan(address)
		rc := p.ResourceChanges[0]
		rc.Type, rc.Name = typ, name
		rc.Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
		rc.Change.Before, rc.Change.After = before, after
		return p
	}
	defaulted := update("aws_sns_topic_subscription.main", "aws_sns_topic_subscription", "main",
		map[string]any{"endpoint": "e", "endpoint_auto_confirms": nil, "confirmation_timeout_in_minutes": nil},
		map[string]any{"endpoint": "e", "endpoint_auto_confirms": false, "confirmation_timeout_in_minutes": float64(1)})
	got := providerDefaults(defaulted, partial)
	if want := []string{"confirmation_timeout_in_minutes", "endpoint_auto_confirms"}; !slices.Equal(got["aws_sns_topic_subscription.main"], want) {
		t.Errorf("defaults: %v, want %v", got, want)
	}

	for name, c := range map[string]struct {
		plan    *tfjson.Plan
		partial map[string][]string
	}{
		"an import alone":         {importPlan("aws_sns_topic_subscription.main"), partial},
		"a resource ignoring all": {defaulted, map[string][]string{}},
		"another resource": {update("aws_sqs_queue.q", "aws_sqs_queue", "q",
			map[string]any{"a": nil}, map[string]any{"a": "x"}), partial},
		"an argument the import read": {update("aws_sns_topic_subscription.main", "aws_sns_topic_subscription", "main",
			map[string]any{"raw_message_delivery": true}, map[string]any{"raw_message_delivery": false}), partial},
		"an argument the plan removes": {update("aws_sns_topic_subscription.main", "aws_sns_topic_subscription", "main",
			map[string]any{"raw_message_delivery": true}, map[string]any{"raw_message_delivery": nil}), partial},
		"a replacement": {func() *tfjson.Plan {
			p := update("aws_sns_topic_subscription.main", "aws_sns_topic_subscription", "main",
				map[string]any{"a": nil}, map[string]any{"a": "x"})
			p.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}
			return p
		}(), partial},
		"no plan": {nil, partial},
	} {
		if got := providerDefaults(c.plan, c.partial); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// TestRemoveStaleSnapshotsSparesDirectoriesInUse checks that a directory
// whose lock a process holds survives the sweep however old it is, the
// long-lived MCP server's, and goes once its holder lets go or ends.
func TestRemoveStaleSnapshotsSparesDirectoriesInUse(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	mcp, err := mkdirTempHeld(tmp, MCPPattern)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := mkdirTempHeld(tmp, DriftRootsPattern)
	if err != nil {
		t.Fatal(err)
	}
	// One of an older version, with no lock file at all.
	legacy := filepath.Join(tmp, DriftPattern+"legacy")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-14 * 24 * time.Hour)
	for _, d := range []string{mcp, roots, legacy} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	removeStaleSnapshots(tmp, time.Now(), 24*time.Hour)
	for dir, want := range map[string]bool{mcp: true, roots: true, legacy: false} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
	// A process that ends without removing its directory releases the lock.
	releaseHeld(roots)
	removeStaleSnapshots(tmp, time.Now(), 24*time.Hour)
	if _, err := os.Stat(roots); err == nil {
		t.Error("the directory whose holder let go was kept")
	}
	if _, err := os.Stat(mcp); err != nil {
		t.Errorf("the directory still in use was removed: %v", err)
	}
	RemoveHeld(mcp)
	if _, err := os.Stat(mcp); err == nil {
		t.Error("RemoveHeld kept the directory")
	}
}

func TestOwnedByCurrentUser(t *testing.T) {
	t.Parallel()
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !ownedByCurrentUser(info) {
		t.Error("a directory the test made isn't the current user's")
	}
}

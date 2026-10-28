// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// testAdapter maps a bucket and its versioning onto a made-up module, and
// declines buckets named "declined".
var testAdapter = adapters.Adapter{
	Source:  "example/bucket/aws",
	Version: "1.0.0",
	Anchor:  "aws_s3_bucket",
	Members: []string{"aws_s3_bucket_versioning"},
	Map: func(c adapters.Cluster) (*adapters.Call, error) {
		name, _ := adapters.Read(c.Anchor.Address(), c.Anchor.Body).Attr("bucket")
		if strings.TrimSpace(string(name.Bytes())) == `"declined"` {
			return nil, adapters.Declinef("declined on purpose")
		}
		call := &adapters.Call{
			Addresses: map[string]string{c.Anchor.Address(): "aws_s3_bucket.this[0]"},
			Outputs:   map[string]map[string]string{c.Anchor.Address(): {"arn": "arn", "bucket": "id"}},
		}
		call.Set("bucket", name)
		for _, m := range c.Members {
			call.Addresses[m.Address()] = m.Type + ".this[0]"
		}
		return call, nil
	},
}

func resourceChange(address string, actions tfjson.Actions, before, after map[string]any) *tfjson.ResourceChange {
	return &tfjson.ResourceChange{Address: address, Mode: tfjson.ManagedResourceMode, Change: &tfjson.Change{Actions: actions, Before: before, After: after}}
}

// importedPlan plans each address for import, with no change.
func importedPlan(addresses ...string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, a := range addresses {
		p.ResourceChanges = append(p.ResourceChanges, resourceChange(a, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	return p
}

// rootChanges is the root's planned changes: imports with no change.
func rootChanges() map[string]string {
	changes := map[string]string{}
	for _, a := range []string{"aws_iam_policy.read", "aws_s3_bucket.logs", "aws_s3_bucket.state", "aws_s3_bucket_versioning.logs", "aws_s3_bucket_versioning.state"} {
		changes[a] = tfrun.ChangeSignature(resourceChange("x."+a, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	return changes
}

var bothCalls = []string{
	"aws_iam_policy.read",
	"module.logs.aws_s3_bucket.this[0]", "module.logs.aws_s3_bucket_versioning.this[0]",
	"module.state.aws_s3_bucket.this[0]", "module.state.aws_s3_bucket_versioning.this[0]",
}

func TestSynthesizeKeepsCallsThatPlanTheSame(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	before, err := importTargets(dir)
	if err != nil {
		t.Fatal(err)
	}

	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges()})
	if err != nil || len(declined) != 0 {
		t.Fatalf("want every cluster moved, got declined=%v err=%v", declined, err)
	}

	root := readFile(t, dir, rootfile.GeneratedFileName)
	for _, want := range []string{
		`module "logs" {`,
		`source  = "example/bucket/aws"`,
		`version = "1.0.0"`,
		`bucket  = "logs"`,
		`module "state" {`,
		"policy = module.logs.arn",
	} {
		if !strings.Contains(squashed(root), squashed(want)) {
			t.Errorf("generated.tf misses %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, `resource "aws_s3_bucket`) {
		t.Errorf("moved resources left in the root:\n%s", root)
	}
	if imports := readFile(t, dir, rootfile.ImportsFileName); !strings.Contains(imports, "to = module.state.aws_s3_bucket_versioning.this[0]") {
		t.Errorf("imports.tf not retargeted:\n%s", imports)
	}
	if got := strings.Join(tf.calls, ","); got != "init,plan,show" {
		t.Errorf("calls: %s", got)
	}

	moved, err := movedAddresses(dir, before)
	if err != nil {
		t.Fatal(err)
	}
	if moved["aws_s3_bucket.logs"] != "module.logs.aws_s3_bucket.this[0]" || len(moved) != 4 {
		t.Errorf("moved: %v", moved)
	}
	calls, err := moduleCalls(dir, moved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Name != "logs" || calls[0].Source != "example/bucket/aws" || calls[0].Version != "1.0.0" ||
		strings.Join(calls[0].Resources, ",") != "aws_s3_bucket.logs,aws_s3_bucket_versioning.logs" {
		t.Errorf("calls: %+v", calls)
	}
}

// The pass leaves the lengths of the list outputs of the calls it kept, those
// that list all their members, for the passes after it to collapse.
func TestSynthesizeLeavesTheListOutputsOfItsCalls(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	listing := testAdapter
	mapOne := testAdapter.Map
	listing.Map = func(c adapters.Cluster) (*adapters.Call, error) {
		call, err := mapOne(c)
		if call != nil {
			call.Outputs = map[string]map[string]string{c.Anchor.Address(): {"bucket": "names[0]"}}
		}
		return call, err
	}

	_, _, lists, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{listing}}, Baseline{changes: rootChanges()})

	// The bucket with a versioning has no list of all its members.
	want := map[string]int{"module.state.names": 1}
	if err != nil || !reflect.DeepEqual(lists, want) {
		t.Errorf("got %v, %v, want %v", lists, err, want)
	}
}

// A call leaves out the arguments set to the module's defaults, as a
// person would call it; before, it set every one the resources had.
func TestSynthesizeLeavesOutModuleDefaults(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	withDefaults := testAdapter
	withDefaults.Defaults = map[string]cty.Value{"bucket": cty.StringVal("logs")}

	if _, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{withDefaults}}, Baseline{changes: rootChanges()}); err != nil {
		t.Fatal(err)
	}
	root := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	if strings.Contains(root, `bucket = "logs"`) || !strings.Contains(root, `bucket = "state"`) {
		t.Errorf("want the default left out of logs's call alone:\n%s", root)
	}
}

// A call whose resources plan differently is taken back; the others stay.
func TestSynthesizeTakesBackCallsThatChangeThePlan(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	first := importedPlan(bothCalls...)
	first.ResourceChanges[3] = resourceChange("module.state.aws_s3_bucket.this[0]", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": nil}, map[string]any{"tags": map[string]any{}})
	second := importedPlan("aws_iam_policy.read", "aws_s3_bucket.state", "aws_s3_bucket_versioning.state", "module.logs.aws_s3_bucket.this[0]", "module.logs.aws_s3_bucket_versioning.this[0]")
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{first, second}}

	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges()})
	if err != nil {
		t.Fatal(err)
	}

	if len(declined) != 1 || !strings.Contains(declined[0].Declined, "aws_s3_bucket.state would plan differently: update (tags)") {
		t.Fatalf("declined: %+v", declined)
	}
	root := readFile(t, dir, rootfile.GeneratedFileName)
	if !strings.Contains(root, `module "logs" {`) || !strings.Contains(root, `resource "aws_s3_bucket" "state" {`) || strings.Contains(root, `module "state"`) {
		t.Errorf("want logs moved and state kept:\n%s", root)
	}
}

// A module may set arguments the provider keeps only in state, which
// import can't: the plan then updates them, and nothing else.
func TestSynthesizeAllowsStateOnlyUpdates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		stateOnly map[string][]string
		kept      bool
	}{
		{"state-only", map[string][]string{"aws_s3_bucket": {"force_destroy"}}, true},
		{"other", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := moduleRoot(t, twoBuckets)
			p := importedPlan(bothCalls...)
			p.ResourceChanges[1] = resourceChange(bothCalls[1], tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"force_destroy": false}, map[string]any{"force_destroy": true})
			p.ResourceChanges[1].Type = "aws_s3_bucket"
			updated := tfrun.ChangeSummary{Change: 1}
			tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: updated}}, showns: []*tfjson.Plan{p}}

			declined, settled, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges(), stateOnly: tc.stateOnly})
			if err != nil {
				t.Fatal(err)
			}
			kept := !strings.Contains(squashed(readFile(t, dir, rootfile.GeneratedFileName)), `resource "aws_s3_bucket" "logs"`)
			if kept != tc.kept {
				t.Errorf("logs moved: %v, want %v; declined %+v", kept, tc.kept, declined)
			}
			if tc.kept && (len(declined) != 0 || settled.summary != updated) {
				t.Errorf("declined %+v, summary %+v", declined, settled.summary)
			}
		})
	}
}

// The passes after the curated modules compare the resources that moved
// into a call with the change the plan accepted for them, by their module
// addresses: the root's addresses are gone, and judged by totals alone an
// edit could change what a moved resource plans, as long as the sums stay.
func TestSynthesizeRebasesTheBaselineOntoTheCalls(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	stateOnly := map[string][]string{"aws_s3_bucket": {"force_destroy"}}
	accepted := importedPlan(bothCalls...)
	accepted.ResourceChanges[1] = resourceChange(bothCalls[1], tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"force_destroy": false}, map[string]any{"force_destroy": true})
	accepted.ResourceChanges[1].Type = "aws_s3_bucket"
	updated := tfrun.ChangeSummary{Change: 1}
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: updated}}, showns: []*tfjson.Plan{accepted}}

	declined, rebased, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges(), stateOnly: stateOnly})
	if err != nil || len(declined) != 0 {
		t.Fatalf("want the calls kept, got declined=%+v err=%v", declined, err)
	}
	if got, want := rebased.Planned(bothCalls[1]), tfrun.ChangeSignature(accepted.ResourceChanges[1]); got != want {
		t.Errorf("the moved bucket: got %q, want %q", got, want)
	}
	if got := rebased.Planned("aws_s3_bucket.logs"); got != "" {
		t.Errorf("the root's address of a moved bucket: got %q, want none", got)
	}
	if rebased.summary != updated {
		t.Errorf("summary: got %+v, want %+v", rebased.summary, updated)
	}

	// A later edit that keeps the totals but changes what the moved bucket
	// plans is undone, as it is for a resource that stayed in the root.
	hides := importedPlan(bothCalls...)
	hides.ResourceChanges[1] = resourceChange(bothCalls[1], tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"force_destroy": false, "tags": map[string]any{}}, map[string]any{"force_destroy": true, "tags": map[string]any{"a": "b"}})
	hides.ResourceChanges[1].Type = "aws_s3_bucket"
	after := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: updated}}, showns: []*tfjson.Plan{hides}}
	before := readFile(t, dir, rootfile.GeneratedFileName)

	kept, err := tidier(after, dir, rebased).settle(tidyPass{}, commentEdit(dir, "edit"))

	if err != nil || kept {
		t.Errorf("got kept=%v err=%v, want the edit undone", kept, err)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != before {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

func TestSynthesizeTakesBackCallsThatCreate(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	p := importedPlan(bothCalls...)
	p.ResourceChanges = append(p.ResourceChanges, resourceChange("module.logs.aws_s3_bucket_public_access_block.this[0]", tfjson.Actions{tfjson.ActionCreate}, nil, map[string]any{}))
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{p, importedPlan(bothCalls[0], "aws_s3_bucket.logs", "aws_s3_bucket_versioning.logs", bothCalls[3], bothCalls[4])}}

	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges()})
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 1 || declined[0].Declined != "the module would also create aws_s3_bucket_public_access_block.this[0]" {
		t.Errorf("declined: %+v", declined)
	}
}

// Without the modules (no registry), nothing moves.
func TestSynthesizeWithoutTheModules(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, initErr: errors.New("registry unreachable")}

	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges()})
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || !strings.Contains(declined[0].Declined, "registry unreachable") {
		t.Errorf("declined: %+v", declined)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

// callsTerraform plans as planFor says for the module calls the root has
// when it plans, by name; the plans show no change.
func callsTerraform(t *testing.T, dir string, planFor func(calls map[string]bool) fakePlan) *fakeTerraform {
	t.Helper()
	tf := &fakeTerraform{dir: dir}
	tf.onPlan = func() {
		f, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]bool{}
		for _, b := range f.Syntax.Blocks {
			if b.Type == "module" && len(b.Labels) == 1 {
				calls[b.Labels[0]] = true
			}
		}
		tf.plans = []fakePlan{planFor(calls)}
	}
	return tf
}

func planCount(tf *fakeTerraform) int {
	n := 0
	for _, c := range tf.calls {
		if c == "plan" {
			n++
		}
	}
	return n
}

var cycle = []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Cycle"}}

// An error the plan can't pin on one call is found by planning the calls
// apart: the call it comes with is declined with it, and the other kept.
func TestSynthesizeOnAPlanError(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	tf := callsTerraform(t, dir, func(calls map[string]bool) fakePlan {
		if calls["state"] {
			return fakePlan{diags: cycle}
		}
		return fakePlan{}
	})

	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges()})
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 1 || declined[0].Declined != "plan error: Cycle" || strings.Join(declined[0].Resources, ",") != "aws_s3_bucket.state,aws_s3_bucket_versioning.state" {
		t.Errorf("declined: %+v", declined)
	}
	// Both calls failed and logs alone didn't: state isn't planned alone.
	if got := strings.Join(tf.calls, ","); got != "init,plan,plan,show" {
		t.Errorf("calls: %s", got)
	}
	root := readFile(t, dir, rootfile.GeneratedFileName)
	if !strings.Contains(root, `module "logs" {`) || !strings.Contains(root, `resource "aws_s3_bucket" "state" {`) || strings.Contains(root, `module "state"`) {
		t.Errorf("want logs moved and state kept:\n%s", root)
	}
}

// bucketRoot is a root of n buckets, b0 on, and their planned changes in
// it: imports with no change.
func bucketRoot(n int) (string, map[string]string) {
	var blocks []string
	changes := map[string]string{}
	for i := range n {
		name := fmt.Sprintf("b%d", i)
		blocks = append(blocks, fmt.Sprintf("resource \"aws_s3_bucket\" %q {\n  bucket = %q\n}\n", name, name))
		changes["aws_s3_bucket."+name] = tfrun.ChangeSignature(resourceChange("x.aws_s3_bucket."+name, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	return strings.Join(blocks, "\n"), changes
}

// A failure the plan can't pin on a call declines only the calls it comes
// with, with its reason, found by planning half of the calls at a time;
// the others are kept, and the plans paid stay few.
func TestSynthesizeFindsTheCallsAPlanFailsFor(t *testing.T) {
	t.Parallel()
	const n = 8
	failsWith := func(bad ...string) func(map[string]bool) fakePlan {
		return func(calls map[string]bool) fakePlan {
			for _, b := range bad {
				if calls[b] {
					return fakePlan{diags: cycle}
				}
			}
			return fakePlan{}
		}
	}
	var all []string
	for i := range n {
		all = append(all, fmt.Sprintf("b%d", i))
	}
	for _, tc := range []struct {
		name     string
		planFor  func(map[string]bool) fakePlan
		declined []string
		reason   string
		// maxPlans is one plan, and two for each halving down to each
		// call declined, or each group planned once for every call.
		maxPlans int
	}{
		{"one bad call", failsWith("b5"), []string{"b5"}, "plan error: Cycle", 1 + 2*3},
		{"two bad calls", failsWith("b1", "b6"), []string{"b1", "b6"}, "plan error: Cycle", 1 + 2*2*3},
		{"every call bad", failsWith(all...), all, "plan error: Cycle", 2*n - 1},
		{"a combination", func(calls map[string]bool) fakePlan {
			if calls["b2"] && calls["b5"] {
				return fakePlan{summary: tfrun.ChangeSummary{Change: 1}}
			}
			return fakePlan{}
		}, []string{"b5"}, "the plan changed outside the module calls", 1 + 2*3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, changes := bucketRoot(n)
			dir, _ := moduleRoot(t, root)
			tf := callsTerraform(t, dir, tc.planFor)

			declined, summary, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: changes})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range declined {
				if d.Declined != tc.reason {
					t.Errorf("%v declined with %q, want %q", d.Resources, d.Declined, tc.reason)
				}
				got = append(got, strings.Join(d.Resources, ","))
			}
			var want []string
			for _, b := range tc.declined {
				want = append(want, "aws_s3_bucket."+b)
			}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("declined %v, want %v", got, want)
			}
			if summary.summary != (tfrun.ChangeSummary{}) {
				t.Errorf("summary: %+v", summary.summary)
			}
			if plans := planCount(tf); plans > tc.maxPlans {
				t.Errorf("%d plans, want at most %d", plans, tc.maxPlans)
			}
			generated := readFile(t, dir, rootfile.GeneratedFileName)
			if len(tc.declined) == n {
				if generated != root {
					t.Errorf("generated.tf not restored:\n%s", generated)
				}
				return
			}
			for _, b := range all {
				called := strings.Contains(generated, fmt.Sprintf("module %q {", b))
				if kept := !strings.Contains(generated, fmt.Sprintf("resource \"aws_s3_bucket\" %q {", b)); called != kept {
					t.Errorf("%s is both a call and a resource, or neither:\n%s", b, generated)
				}
				if isDeclined := slices.Contains(tc.declined, b); called == isDeclined {
					t.Errorf("%s called: %v, declined: %v:\n%s", b, called, declined, generated)
				}
			}
		})
	}
}

// A plan that reports neither errors nor a result tells nothing: the calls
// are taken back, not planned again.
func TestSynthesizeWhenThePlanReportsNothing(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{nil}}

	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, Baseline{changes: rootChanges()})
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || declined[0].Declined != "the plan reported no result" || declined[1].Declined != "the plan reported no result" {
		t.Errorf("declined: %+v", declined)
	}
	if got := strings.Join(tf.calls, ","); got != "init,plan,show" {
		t.Errorf("calls: %s", got)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

func TestMapClustersDeclines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		root, reason string
	}{
		"by the adapter": {
			strings.Replace(twoBuckets, `bucket = "state"`, `bucket = "declined"`, 1),
			"declined on purpose",
		},
		"a reference without an output": {
			strings.Replace(twoBuckets, "policy = aws_s3_bucket.logs.arn", "policy = aws_s3_bucket.logs.region", 1),
			"the module has no output for a reference to aws_s3_bucket.logs.region",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := moduleRoot(t, tc.root)
			trials, declined, err := mapClusters(dir, []adapters.Adapter{testAdapter}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(trials) != 1 || len(declined) != 1 || declined[0].Declined != tc.reason {
				t.Errorf("trials=%d declined=%+v", len(trials), declined)
			}
		})
	}
}

// A reference to a string the module has no output for, such as an
// alarm's queue name, becomes the string again: the call matters more.
func TestMapClustersWritesStringsWithoutOutputs(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, strings.Replace(twoBuckets, "policy = aws_s3_bucket.logs.arn", "policy = aws_s3_bucket.logs.region", 1))
	values := map[string]map[string]any{"aws_s3_bucket.logs": {"region": "eu-west-2"}}
	trials, declined, err := mapClusters(dir, []adapters.Adapter{testAdapter}, nil, values)
	if err != nil || len(trials) != 2 || len(declined) != 0 {
		t.Fatalf("trials=%d declined=%+v err=%v", len(trials), declined, err)
	}
	if err := writeCalls(dir, trials, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := squashed(readFile(t, dir, rootfile.GeneratedFileName)); !strings.Contains(got, `policy = "eu-west-2"`) {
		t.Errorf("generated.tf:\n%s", got)
	}
}

func squashed(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// References become references to module outputs, or to paths into them,
// wherever they are in an expression; longer names and data sources stay.
func TestSpliceTraversal(t *testing.T) {
	t.Parallel()
	replacement, err := outputTokens("net", `subnets["us-central1/app"].self_link`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "google_compute_instance" "vm" {
  subnetwork = google_compute_subnetwork.app.self_link
  labels     = { link = "${google_compute_subnetwork.app.self_link}/x" }
  other      = google_compute_subnetwork.app.self_link_2
  data_one   = data.google_compute_subnetwork.app.self_link
  network_interface {
    subnetwork = [google_compute_subnetwork.app.self_link, google_compute_subnetwork.app_2.self_link]
  }
}
`)
	f, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Resources() {
		replaceReferences(r.Write.Body(), []string{"google_compute_subnetwork", "app", "self_link"}, replacement)
	}
	got := string(hclwrite.Format(f.Edit().Bytes()))
	for _, want := range []string{
		`subnetwork = module.net.subnets["us-central1/app"].self_link`,
		`link = "${module.net.subnets["us-central1/app"].self_link}/x"`,
		"other      = google_compute_subnetwork.app.self_link_2",
		"data_one   = data.google_compute_subnetwork.app.self_link",
		`subnetwork = [module.net.subnets["us-central1/app"].self_link, google_compute_subnetwork.app_2.self_link]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A kept call whose module uses another provider configures it in the
// root; when every call is taken back, so is the provider.
func TestSynthesizeConfiguresModuleProviders(t *testing.T) {
	t.Parallel()
	withBeta := testAdapter
	withBeta.Providers = []string{"google-beta"}
	moduleProviders := map[string]rootfile.Provider{"google-beta": {Name: "google-beta", Source: "hashicorp/google-beta", Config: map[string]interface{}{"project": "acme"}}}

	dir, _ := moduleRoot(t, twoBuckets)
	writeConfig(t, dir, rootfile.ProvidersFileName, "provider \"aws\" {\n  region = \"eu-west-2\"\n}\n")
	// What init selected, which pins the provider as the root's is.
	writeConfig(t, dir, tfrun.LockFileName, "provider \"registry.terraform.io/hashicorp/google-beta\" {\n  version = \"8.6.0\"\n}\n")
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	if _, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{withBeta}, ModuleProviders: moduleProviders}, Baseline{changes: rootChanges()}); err != nil {
		t.Fatal(err)
	}
	if got := squashed(readFile(t, dir, rootfile.ProvidersFileName)); !strings.Contains(got, squashed(`provider "google-beta" { project = "acme" }`)) || !strings.Contains(got, `provider "aws"`) {
		t.Errorf("providers.tf:\n%s", got)
	}
	if got := squashed(readFile(t, dir, rootfile.VersionsFileName)); !strings.Contains(got, squashed(`google-beta = { source = "hashicorp/google-beta" version = "~> 8.6" }`)) {
		t.Errorf("versions.tf:\n%s", got)
	}

	dir, _ = moduleRoot(t, twoBuckets)
	writeConfig(t, dir, rootfile.ProvidersFileName, "provider \"aws\" {\n  region = \"eu-west-2\"\n}\n")
	failing := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{nil}}
	if _, _, _, err := synthesizeRoot(failing, dir, Options{Adapters: []adapters.Adapter{withBeta}, ModuleProviders: moduleProviders}, Baseline{changes: rootChanges()}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, rootfile.ProvidersFileName) + readFile(t, dir, rootfile.VersionsFileName); strings.Contains(got, "google-beta") {
		t.Errorf("a provider of no kept call:\n%s", got)
	}
}

// Under google --beta, whose root configures google-beta, a kept call
// whose module uses google gets a google block for the same project and
// region, pinned, that applies the root's default labels as google-beta
// does; google-beta's block stays as it was.
func TestSynthesizeConfiguresTheRootsCounterpartUnderBeta(t *testing.T) {
	t.Parallel()
	withGoogle := testAdapter
	withGoogle.Providers = []string{"google", "google-beta"}
	config := map[string]interface{}{"project": "acme", "region": "europe-west1", "add_terraform_attribution_label": false}
	moduleProviders := map[string]rootfile.Provider{"google": {Name: "google", Source: "hashicorp/google", Config: config}}
	labels := &DefaultTags{Provider: "google-beta", Attribute: "labels", Block: "default_labels", Argument: true, ReservedPrefix: "goog-"}

	dir, _ := moduleRoot(t, twoBuckets)
	const rootProviders = "provider \"google-beta\" {\n  add_terraform_attribution_label = false\n  default_labels                  = local.labels\n  project                         = \"acme\"\n  region                          = \"europe-west1\"\n}\n"
	writeConfig(t, dir, rootfile.ProvidersFileName, rootProviders)
	writeConfig(t, dir, rootfile.VersionsFileName, "terraform {\n  required_providers {\n    google-beta = {\n      source  = \"hashicorp/google-beta\"\n      version = \"~> 7.2\"\n    }\n  }\n}\n")
	writeConfig(t, dir, tfrun.LockFileName, "provider \"registry.terraform.io/hashicorp/google\" {\n  version = \"7.2.0\"\n}\n")
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	if _, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{withGoogle}, ModuleProviders: moduleProviders, DefaultTags: labels}, Baseline{changes: rootChanges()}); err != nil {
		t.Fatal(err)
	}
	want := rootProviders + "\nprovider \"google\" {\n  add_terraform_attribution_label = false\n  project                         = \"acme\"\n  region                          = \"europe-west1\"\n  default_labels                  = local.labels\n}\n"
	if got := readFile(t, dir, rootfile.ProvidersFileName); got != want {
		t.Errorf("providers.tf:\n%s\nwant:\n%s", got, want)
	}
	if got := squashed(readFile(t, dir, rootfile.VersionsFileName)); !strings.Contains(got, squashed(`google = { source = "hashicorp/google" version = "~> 7.2" }`)) || !strings.Contains(got, squashed(`google-beta = { source = "hashicorp/google-beta" version = "~> 7.2" }`)) {
		t.Errorf("versions.tf:\n%s", got)
	}
}

// Under google --beta, a call of the organisation's own module in a
// directory gets a configured google block when the module requires
// google, and none when it requires only google-beta, as the adapter
// Generic reads from the module says (see adapters.Adapter.Providers).
func TestSynthesizeConfiguresOwnModulesProvidersUnderBeta(t *testing.T) {
	t.Parallel()
	const generated = `resource "google_storage_bucket" "logs" {
  provider = google-beta
  location = "EU"
  name     = "logs"
}
`
	const bucket = `resource "google_storage_bucket" "this" {
  location = var.location
  name     = var.name
}

variable "location" {
  type = string
}

variable "name" {
  type = string
}
`
	const rootProviders = "provider \"google-beta\" {\n  project = \"acme\"\n}\n"
	config := map[string]interface{}{"project": "acme"}
	moduleProviders := map[string]rootfile.Provider{"google": {Name: "google", Source: "hashicorp/google", Config: config}}
	changes := map[string]string{"google_storage_bucket.logs": tfrun.ChangeSignature(resourceChange("x", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
	for _, tc := range []struct {
		requires   string
		wantGoogle bool
	}{
		{requires: "google", wantGoogle: true},
		{requires: "google-beta", wantGoogle: false},
	} {
		module := t.TempDir()
		writeConfig(t, module, "main.tf", bucket+"\nterraform {\n  required_providers {\n    "+tc.requires+" = {\n      source = \"hashicorp/"+tc.requires+"\"\n    }\n  }\n}\n")
		own, err := adapters.Generic(module, "", module)
		if err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		f := writeConfig(t, dir, rootfile.GeneratedFileName, generated)
		var imports []rootfile.Import
		for _, r := range f.Resources() {
			typ, name, _ := strings.Cut(r.Address, ".")
			imports = append(imports, rootfile.Import{Type: typ, Name: name, ID: name, Provider: "google-beta"})
		}
		content, err := rootfile.ImportsFile(imports)
		if err != nil {
			t.Fatal(err)
		}
		writeConfig(t, dir, rootfile.ImportsFileName, string(content))
		writeConfig(t, dir, rootfile.ProvidersFileName, rootProviders)
		writeConfig(t, dir, rootfile.VersionsFileName, "terraform {\n  required_providers {\n    google-beta = {\n      source  = \"hashicorp/google-beta\"\n      version = \"~> 7.2\"\n    }\n  }\n}\n")
		writeConfig(t, dir, tfrun.LockFileName, "provider \"registry.terraform.io/hashicorp/google\" {\n  version = \"7.2.0\"\n}\n")
		tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan("module.logs.google_storage_bucket.this")}}
		calls, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{own}, ModuleProviders: moduleProviders}, Baseline{changes: changes})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(readFile(t, dir, rootfile.GeneratedFileName), `module "logs"`) {
			t.Fatalf("requires %s: the call wasn't kept: %+v\n%s", tc.requires, calls, readFile(t, dir, rootfile.GeneratedFileName))
		}
		providers, versions := readFile(t, dir, rootfile.ProvidersFileName), squashed(readFile(t, dir, rootfile.VersionsFileName))
		if !tc.wantGoogle {
			if providers != rootProviders || strings.Contains(versions, "hashicorp/google\"") {
				t.Errorf("requires %s: providers.tf:\n%s\nversions.tf:\n%s", tc.requires, providers, versions)
			}
			continue
		}
		if want := rootProviders + "\nprovider \"google\" {\n  project = \"acme\"\n}\n"; providers != want {
			t.Errorf("requires %s: providers.tf:\n%s\nwant:\n%s", tc.requires, providers, want)
		}
		if !strings.Contains(versions, squashed(`google = { source = "hashicorp/google" version = "~> 7.2" }`)) {
			t.Errorf("requires %s: versions.tf:\n%s", tc.requires, versions)
		}
	}
}

// networkAdapter maps a VPC, and what joins it through its subnet, onto a
// made-up module that keeps the route table's route as a resource of its
// own.
var networkAdapter = adapters.Adapter{
	Source:   "example/network/aws",
	Version:  "1.0.0",
	Anchor:   "aws_vpc",
	Members:  []string{"aws_subnet", "aws_route_table", "aws_route_table_association", "aws_nat_gateway", "aws_eip"},
	Repeated: []string{"aws_subnet", "aws_route_table_association"},
	Through:  []string{"aws_route_table_association", "aws_nat_gateway", "aws_eip"},
	Map: func(c adapters.Cluster) (*adapters.Call, error) {
		call := &adapters.Call{
			Addresses: map[string]string{c.Anchor.Address(): "aws_vpc.this[0]"},
			Outputs:   map[string]map[string]string{c.Anchor.Address(): {"id": "vpc_id"}},
			Imports:   []adapters.Import{{To: "aws_route.this[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0"}},
		}
		for _, m := range c.Members {
			call.Addresses[m.Address()] = m.Type + `.this["` + m.Name + `"]`
		}
		return call, nil
	},
}

const network = `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "a" {
  cidr_block = "10.0.1.0/24"
  vpc_id     = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table_association" "a" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.a.id
}

resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.a.id
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_eip" "other" {
  domain = "vpc"
}

resource "aws_security_group" "web" {
  vpc_id = aws_vpc.main.id
}
`

// Members join through the cluster's other resources, in either
// direction, and the call imports what the module keeps separately.
func TestSynthesizeJoinsThroughMembersAndAddsImports(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, network)
	trials, declined, err := mapClusters(dir, []adapters.Adapter{networkAdapter}, nil, nil)
	if err != nil || len(declined) != 0 || len(trials) != 1 {
		t.Fatalf("trials=%d declined=%+v err=%v", len(trials), declined, err)
	}
	members := trials[0].members()
	if got := strings.Join(members, ","); got != "aws_vpc.main,aws_eip.nat,aws_nat_gateway.main,aws_route_table.public,aws_route_table_association.a,aws_subnet.a" {
		t.Fatalf("members: %s", got)
	}

	changes := map[string]string{}
	addresses := []string{"aws_eip.other", "aws_security_group.web", "module.vpc.aws_route.this[0]"}
	for _, m := range members {
		changes[m] = tfrun.ChangeSignature(resourceChange("x."+m, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
		addresses = append(addresses, trials[0].moduleAddress(m))
	}
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: tfrun.ChangeSummary{Import: 1}}}, showns: []*tfjson.Plan{importedPlan(addresses...)}}
	declined, summary, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{networkAdapter}}, Baseline{changes: changes})
	if err != nil || len(declined) != 0 {
		t.Fatalf("want the call kept, got declined=%+v err=%v", declined, err)
	}
	if summary.summary.Import != 1 {
		t.Errorf("summary: %+v", summary.summary)
	}
	imports := squashed(readFile(t, dir, rootfile.ImportsFileName))
	for _, want := range []string{
		`to = module.vpc.aws_route.this[0] id = "public_0.0.0.0/0"`,
		`to = module.vpc.aws_eip.this["nat"] id = "nat"`,
		`to = aws_eip.other id = "other"`,
	} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	if root := squashed(readFile(t, dir, rootfile.GeneratedFileName)); !strings.Contains(root, "vpc_id = module.vpc.vpc_id") {
		t.Errorf("generated.tf:\n%s", root)
	}
}

// The added imports count: a plan that imports only the root's resources
// doesn't match.
func TestSynthesizeExpectsTheAddedImports(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, network)
	trials, _, err := mapClusters(dir, []adapters.Adapter{networkAdapter}, nil, nil)
	if err != nil || len(trials) != 1 {
		t.Fatal(trials, err)
	}
	changes := map[string]string{}
	addresses := []string{"aws_eip.other", "aws_security_group.web"}
	for _, m := range trials[0].members() {
		changes[m] = tfrun.ChangeSignature(resourceChange("x."+m, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
		addresses = append(addresses, trials[0].moduleAddress(m))
	}
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(addresses...)}}
	declined, _, _, err := synthesizeRoot(tf, dir, Options{Adapters: []adapters.Adapter{networkAdapter}}, Baseline{changes: changes})
	if err != nil || len(declined) != 1 || declined[0].Declined != "the plan changed outside the module calls" {
		t.Fatalf("declined=%+v err=%v", declined, err)
	}
	if strings.Contains(readFile(t, dir, rootfile.ImportsFileName), "aws_route.this") {
		t.Error("imports.tf keeps the added import")
	}
}

// Members the module doesn't manage stay in the root.
func TestMapClustersLeavesWhatTheAdapterLeaves(t *testing.T) {
	t.Parallel()
	leaving := networkAdapter
	leaving.Map = func(c adapters.Cluster) (*adapters.Call, error) {
		call, err := networkAdapter.Map(c)
		if err != nil {
			return nil, err
		}
		delete(call.Addresses, "aws_eip.nat")
		call.Left = []string{"aws_eip.nat"}
		return call, nil
	}
	dir, _ := moduleRoot(t, network)
	trials, declined, err := mapClusters(dir, []adapters.Adapter{leaving}, nil, nil)
	if err != nil || len(declined) != 0 || len(trials) != 1 {
		t.Fatalf("trials=%d declined=%+v err=%v", len(trials), declined, err)
	}
	if got := strings.Join(trials[0].members(), ","); got != "aws_vpc.main,aws_nat_gateway.main,aws_route_table.public,aws_route_table_association.a,aws_subnet.a" {
		t.Errorf("members: %s", got)
	}
}

// grantsAdapter maps a project's grants, grouped by their project, onto a
// made-up module; it declines the project "declined".
var grantsAdapter = adapters.Adapter{
	Source:   "example/grants/google",
	Version:  "1.0.0",
	Anchor:   "google_project_iam_member",
	Members:  []string{"google_project_iam_member"},
	Repeated: []string{"google_project_iam_member"},
	Group: func(r adapters.Resource) (string, bool) {
		attr := r.Body.GetAttribute("project")
		if attr == nil {
			return "", false
		}
		return string(attr.Expr().BuildTokens(nil).Bytes()), true
	},
	Map: func(c adapters.Cluster) (*adapters.Call, error) {
		if strings.Contains(string(c.Anchor.Body.GetAttribute("project").Expr().BuildTokens(nil).Bytes()), "declined") {
			return nil, adapters.Declinef("declined on purpose")
		}
		call := &adapters.Call{Addresses: map[string]string{c.Anchor.Address(): `google_project_iam_member.this["` + c.Anchor.Name + `"]`}}
		for _, m := range c.Members {
			call.Addresses[m.Address()] = `google_project_iam_member.this["` + m.Name + `"]`
		}
		return call, nil
	},
}

// Resources with the same key join one cluster without referring to each
// other; one without a key stays out, and a declined group is tried once.
func TestMapClustersGroups(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, `resource "google_project_iam_member" "a" {
  project = "acme"
}

resource "google_project_iam_member" "b" {
  project = "other"
}

resource "google_project_iam_member" "c" {
  project = "acme"
}

resource "google_project_iam_member" "d" {
  member = "user:x"
}

resource "google_project_iam_member" "e" {
  project = "declined"
}

resource "google_project_iam_member" "f" {
  project = "declined"
}
`)
	trials, declined, err := mapClusters(dir, []adapters.Adapter{grantsAdapter}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var clusters []string
	for _, tr := range trials {
		clusters = append(clusters, strings.Join(tr.members(), ","))
	}
	if got := strings.Join(clusters, " "); got != "google_project_iam_member.a,google_project_iam_member.c google_project_iam_member.b" {
		t.Errorf("clusters: %s", got)
	}
	if len(declined) != 1 || strings.Join(declined[0].Resources, ",") != "google_project_iam_member.e,google_project_iam_member.f" {
		t.Errorf("declined: %+v", declined)
	}
}

// A call the adapter names is named so, made unique, rather than after its
// anchor, which is only the first resource of a group.
func TestMapClustersNamesCallsAsTheAdapterSays(t *testing.T) {
	t.Parallel()
	dir, _ := moduleRoot(t, `resource "google_project_iam_member" "a" {
  project = "acme"
}

resource "google_project_iam_member" "b" {
  project = "other"
}
`)
	named := grantsAdapter
	named.Map = func(c adapters.Cluster) (*adapters.Call, error) {
		call, err := grantsAdapter.Map(c)
		if call != nil {
			call.Name = "grants"
		}
		return call, err
	}
	trials, _, err := mapClusters(dir, []adapters.Adapter{named}, Names{"module.grants": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range trials {
		names = append(names, tr.name)
	}
	if got := strings.Join(names, " "); got != "grants_2 grants_3" {
		t.Errorf("calls named %s, want grants_2 grants_3: grants is taken", got)
	}
}

// A module in a directory is called by its path from the root.
func TestCallSource(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	root := filepath.Join(out, "aws", "123", "eu-west-2")
	for source, want := range map[string]string{
		filepath.Join(out, "modules", "bucket"): "../../../modules/bucket",
		filepath.Join(root, "local"):            "./local",
		"git::https://example.com/m.git?ref=v1": "git::https://example.com/m.git?ref=v1",
		"terraform-aws-modules/vpc/aws":         "terraform-aws-modules/vpc/aws",
	} {
		if got, err := hclfile.CallSource(root, source); err != nil || got != want {
			t.Errorf("hclfile.CallSource(%s) = %s, %v; want %s", source, got, err, want)
		}
	}
}

// Members that use another configuration of the provider are mapped
// without it, and those that use the default one, such as a bucket's
// versioning, which takes no tags, go with them; two other configurations
// share none.
func TestSharedProvider(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), rootfile.GeneratedFileName, `resource "aws_s3_bucket" "logs" {
  bucket   = "logs"
  provider = aws.without_default_tags
}

resource "aws_s3_bucket_versioning" "logs" {
  bucket = aws_s3_bucket.logs.id
}

resource "aws_s3_bucket_versioning" "other" {
  bucket   = aws_s3_bucket.logs.id
  provider = aws.other
}
`)
	r := f.Resources()
	bucket, versioning := resourceOf(r[0]), resourceOf(r[1])
	c := adapters.Cluster{Anchor: bucket, Members: []adapters.Resource{versioning}}
	provider, setting := sharedProvider(c)
	if got := strings.TrimSpace(string(provider.Bytes())); got != "aws.without_default_tags" {
		t.Fatalf("shared provider: got %q", got)
	}
	if len(setting) != 1 || setting[0].Address() != bucket.Address() {
		t.Fatalf("want only the bucket setting it, got %v", setting)
	}
	setProvider(setting, nil)
	if bucket.Body.GetAttribute("provider") != nil {
		t.Error("the provider stays on the bucket")
	}
	setProvider(setting, provider)
	if bucket.Body.GetAttribute("provider") == nil {
		t.Error("the provider isn't put back")
	}
	if versioning.Body.GetAttribute("provider") != nil {
		t.Error("the provider is put on a member that had none")
	}
	mixed := adapters.Cluster{Anchor: bucket, Members: []adapters.Resource{resourceOf(r[2])}}
	if p, _ := sharedProvider(mixed); p != nil {
		t.Error("members with different providers share one")
	}
	if p, _ := sharedProvider(adapters.Cluster{Anchor: versioning}); p != nil {
		t.Error("a cluster on the default configuration shares another")
	}
}

// The references between a root's resources are read once, for every
// anchor and trial to look up: who refers to whom, and from where.
func TestIndexReferences(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), rootfile.GeneratedFileName, `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
}

resource "aws_s3_bucket_versioning" "logs" {
  bucket = aws_s3_bucket.logs.id
  versioning_configuration {
    status = var.status
  }
}

resource "aws_s3_bucket_policy" "logs" {
  bucket = aws_s3_bucket.logs.bucket
  policy = aws_s3_bucket_versioning.logs.id
}
`)
	resources := f.Resources()
	refs := indexReferences(resources)

	var referring []string
	for _, r := range refs.referrers("aws_s3_bucket.logs", resources) {
		referring = append(referring, r.Address)
	}
	if got, want := strings.Join(referring, ","), "aws_s3_bucket_versioning.logs,aws_s3_bucket_policy.logs"; got != want {
		t.Errorf("referrers: got %s, want %s", got, want)
	}
	if !refs.refers["aws_s3_bucket_policy.logs"]["aws_s3_bucket_versioning.logs"] || refs.refers["aws_s3_bucket.logs"]["aws_s3_bucket_policy.logs"] {
		t.Errorf("refers: %v", refs.refers)
	}
	if len(refs.incoming["aws_s3_bucket_versioning.logs"]) != 1 || refs.incoming["aws_s3_bucket_versioning.logs"][0].from != "aws_s3_bucket_policy.logs" {
		t.Errorf("incoming: %+v", refs.incoming["aws_s3_bucket_versioning.logs"])
	}
	// Linked either way, and not to what it has no reference with.
	bucket := resources[:1]
	if !refs.linked("aws_s3_bucket_versioning.logs", bucket) || !refs.linked("aws_s3_bucket.logs", resources[1:2]) {
		t.Error("versioning and bucket not linked")
	}
	if refs.linked("aws_s3_bucket_policy.logs", resources[1:1]) {
		t.Error("linked to an empty cluster")
	}
}

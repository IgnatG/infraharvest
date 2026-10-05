// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/adapters"
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
		changes[a] = changeSignature(resourceChange("x."+a, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	return changes
}

var bothCalls = []string{
	"aws_iam_policy.read",
	"module.logs.aws_s3_bucket.this[0]", "module.logs.aws_s3_bucket_versioning.this[0]",
	"module.state.aws_s3_bucket.this[0]", "module.state.aws_s3_bucket_versioning.this[0]",
}

func TestSynthesizeKeepsCallsThatPlanTheSame(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	before, err := importTargets(dir)
	if err != nil {
		t.Fatal(err)
	}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil || len(declined) != 0 {
		t.Fatalf("want every cluster moved, got declined=%v err=%v", declined, err)
	}

	root := readFile(t, dir, GeneratedFileName)
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
	if imports := readFile(t, dir, ImportsFileName); !strings.Contains(imports, "to = module.state.aws_s3_bucket_versioning.this[0]") {
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

// A call whose resources plan differently is taken back; the others stay.
func TestSynthesizeTakesBackCallsThatChangeThePlan(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	first := importedPlan(bothCalls...)
	first.ResourceChanges[3] = resourceChange("module.state.aws_s3_bucket.this[0]", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": nil}, map[string]any{"tags": map[string]any{}})
	second := importedPlan("aws_iam_policy.read", "aws_s3_bucket.state", "aws_s3_bucket_versioning.state", "module.logs.aws_s3_bucket.this[0]", "module.logs.aws_s3_bucket_versioning.this[0]")
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{first, second}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(declined) != 1 || !strings.Contains(declined[0].Declined, "aws_s3_bucket.state would plan differently: update (tags)") {
		t.Fatalf("declined: %+v", declined)
	}
	root := readFile(t, dir, GeneratedFileName)
	if !strings.Contains(root, `module "logs" {`) || !strings.Contains(root, `resource "aws_s3_bucket" "state" {`) || strings.Contains(root, `module "state"`) {
		t.Errorf("want logs moved and state kept:\n%s", root)
	}
}

// A module may set arguments the provider keeps only in state, which
// import can't: the plan then updates them, and nothing else.
func TestSynthesizeAllowsStateOnlyUpdates(t *testing.T) {
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
			updated := changeSummary{Change: 1}
			tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: updated}}, showns: []*tfjson.Plan{p}}

			declined, settled, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}, StateOnly: tc.stateOnly}, changeSummary{}, rootChanges(), nil)
			if err != nil {
				t.Fatal(err)
			}
			kept := !strings.Contains(squashed(readFile(t, dir, GeneratedFileName)), `resource "aws_s3_bucket" "logs"`)
			if kept != tc.kept {
				t.Errorf("logs moved: %v, want %v; declined %+v", kept, tc.kept, declined)
			}
			if tc.kept && (len(declined) != 0 || settled != updated) {
				t.Errorf("declined %+v, summary %+v", declined, settled)
			}
		})
	}
}

func TestSynthesizeTakesBackCallsThatCreate(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	p := importedPlan(bothCalls...)
	p.ResourceChanges = append(p.ResourceChanges, resourceChange("module.logs.aws_s3_bucket_public_access_block.this[0]", tfjson.Actions{tfjson.ActionCreate}, nil, map[string]any{}))
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{p, importedPlan(bothCalls[0], "aws_s3_bucket.logs", "aws_s3_bucket_versioning.logs", bothCalls[3], bothCalls[4])}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 1 || declined[0].Declined != "the module would also create aws_s3_bucket_public_access_block.this[0]" {
		t.Errorf("declined: %+v", declined)
	}
}

// Without the modules (no registry), nothing moves.
func TestSynthesizeWithoutTheModules(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, initErr: errors.New("registry unreachable")}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || !strings.Contains(declined[0].Declined, "registry unreachable") {
		t.Errorf("declined: %+v", declined)
	}
	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

// An error the plan can't pin on one call takes them all back.
func TestSynthesizeOnAPlanError(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Cycle"}}}}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || declined[1].Declined != "plan error: Cycle" {
		t.Errorf("declined: %+v", declined)
	}
	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

func TestMapClustersDeclines(t *testing.T) {
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
			trials, declined, err := mapClusters(dir, []adapters.Adapter{testAdapter}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(trials) != 1 || len(declined) != 1 || declined[0].Declined != tc.reason {
				t.Errorf("trials=%d declined=%+v", len(trials), declined)
			}
		})
	}
}

func squashed(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

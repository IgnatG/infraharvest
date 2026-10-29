// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// newTestPhase returns a merge phase of the output out, through tools,
// that records into a report of its own, its failures among them.
func newTestPhase(out string, tools *callTools) *mergePhase {
	p := &mergePhase{out: out, tools: tools, schemas: &tfrun.SchemaCache{}, report: &report.Report{}}
	p.fail = p.report.AddFailure
	return p
}

// fakeTerraform is a Terraform runner that plans nothing, and records
// the directories it was asked to run in, and how often it planned.
type fakeTerraform struct {
	mu    sync.Mutex
	dirs  []string
	plans int
}

func (f *fakeTerraform) in(dir string) (tfrun.Terraform, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs = append(f.dirs, dir)
	return f, nil
}

func (f *fakeTerraform) Init(context.Context, ...tfexec.InitOption) error { return nil }

func (f *fakeTerraform) PlanJSON(_ context.Context, w io.Writer, _ ...tfexec.PlanOption) (bool, error) {
	f.mu.Lock()
	f.plans++
	f.mu.Unlock()
	_, err := io.WriteString(w, `{"type":"change_summary","changes":{"add":0,"change":0,"remove":0,"import":0}}`+"\n")
	return false, err
}

func (f *fakeTerraform) Validate(context.Context) (*tfjson.ValidateOutput, error) {
	return &tfjson.ValidateOutput{Valid: true}, nil
}

func (f *fakeTerraform) ProvidersSchema(context.Context) (*tfjson.ProviderSchemas, error) {
	return &tfjson.ProviderSchemas{}, nil
}

func (f *fakeTerraform) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	return &tfjson.Plan{}, nil
}

func (f *fakeTerraform) FormatCheck(context.Context, ...tfexec.FormatOption) (bool, []string, error) {
	return true, nil, nil
}

func (f *fakeTerraform) SetStdout(io.Writer) {}

// The merge phase merges each stack of its members with the Terraform
// its tools give each environment, which plans that environment in the
// merged root, and records the outcome, or the failure, in its report.
func TestMergePhaseRun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failWith error
	}{
		{name: "merged"},
		{name: "no credentials", failWith: errors.New("no credentials")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			f := &fakeEngine{}
			p, stack, members := stackMembers(t, out, f, "prod", "dev")
			for i := range members {
				members[i].execPath, members[i].cacheDir = "terraform", filepath.Join(out, ".cache")
			}
			p.enabled, p.members = true, members
			tf := &fakeTerraform{}
			var runs []terraformRun
			p.tools.terraform = func(_ context.Context, tr terraformRun, _ harvest.Provider) (func(string) (tfrun.Terraform, error), error) {
				runs = append(runs, tr)
				return tf.in, tc.failWith
			}
			// The merge plans each environment with its own Terraform.
			merge := p.tools.merge
			p.tools.merge = func(ctx context.Context, dir string, roots []envmerge.MergeRoot, opts envmerge.MergeOptions) (*envmerge.MergeResult, error) {
				for _, r := range roots {
					runner, err := r.Terraform(dir)
					if err != nil {
						return nil, err
					}
					if _, err := runner.PlanJSON(ctx, io.Discard); err != nil {
						return nil, err
					}
				}
				return merge(ctx, dir, roots, opts)
			}
			if err := p.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tc.failWith != nil {
				if len(p.report.Failures) != 1 || !strings.Contains(p.report.Failures[0], "merging app: no credentials") {
					t.Errorf("failures %v", p.report.Failures)
				}
				if len(f.merged) != 0 || tf.plans != 0 {
					t.Errorf("merged %d times, planned %d, want neither", len(f.merged), tf.plans)
				}
				return
			}
			if len(p.report.Failures) != 0 {
				t.Fatalf("failures %v", p.report.Failures)
			}
			if len(runs) != 2 || runs[0].pluginCacheDir != filepath.Join(out, ".cache", "plugins") || runs[0].schemas != p.schemas {
				t.Errorf("Terraform runs %+v", runs)
			}
			if tf.plans != 2 || !slices.Equal(tf.dirs, []string{stack, stack}) {
				t.Errorf("planned %d times in %v, want each environment in %s", tf.plans, tf.dirs, stack)
			}
			if len(f.merged) != 1 || f.merged[0][0].Env != "dev" || f.merged[0][1].Env != "prod" {
				t.Errorf("merged %+v, want dev then prod", f.merged)
			}
			if m := p.report.Merges; len(m) != 1 || !m[0].Merged || m[0].Path != "app" {
				t.Errorf("merges %+v", m)
			}
			if c, err := mergeCheckpoints.read(out, stack); err != nil || c == nil || !c.Merge.Merged {
				t.Errorf("merge checkpoint %+v, %v", c, err)
			}
		})
	}
}

// Without --merge-environments, the merge phase merges nothing.
func TestMergePhaseRunDisabled(t *testing.T) {
	out := t.TempDir()
	f := &fakeEngine{}
	p, _, members := stackMembers(t, out, f, "dev", "prod")
	p.members = members
	if err := p.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.merged) != 0 || len(p.report.Merges) != 0 || len(p.report.Failures) != 0 {
		t.Errorf("merged %d times, merges %+v, failures %v", len(f.merged), p.report.Merges, p.report.Failures)
	}
}

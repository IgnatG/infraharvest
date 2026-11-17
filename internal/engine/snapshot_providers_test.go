// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

const (
	plainProviders  = "provider \"aws\" {\n  region = \"us-east-1\"\n}\n"
	taggedProviders = "provider \"aws\" {\n  region = \"us-east-1\"\n\n  default_tags {\n    tags = {\n      team = \"a\"\n    }\n  }\n}\n"
)

// cloudModel models what a provider reads: a state, from an import plan
// applied, holds the resource as the provider that built it read it, and a
// provider with default_tags reads tags without the default ones, so a
// plan that reads the state of a snapshot built before the provider had
// them shows an update of the tags, where a plan that imports shows none.
// One cloudModel is the runner of every directory of a test, and counts.
type cloudModel struct {
	tfrun.Terraform
	mu sync.Mutex
	// states are the provider blocks each state file was built with.
	states map[string]string
	// last is the plan the last PlanJSON made.
	last *tfjson.Plan

	plans, cachedPlans, applies int
}

// modelRunner is the cloudModel's runner of dir.
type modelRunner struct {
	*cloudModel
	dir string
}

func newCloudModel() *cloudModel {
	return &cloudModel{states: map[string]string{}}
}

func (m *modelRunner) Init(context.Context, ...tfexec.InitOption) error { return nil }

func (m *modelRunner) SetStdout(io.Writer) {}

func (m *modelRunner) providers() string {
	content, err := os.ReadFile(filepath.Join(m.dir, "providers.tf"))
	if err != nil {
		return ""
	}
	return string(content)
}

// optionPath is the path an option of tfexec has, which it doesn't export.
func optionPath(opt any) (string, bool) {
	v := reflect.ValueOf(opt)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return "", false
	}
	if n := v.Elem().Type().Name(); n != "StateOption" && n != "StateOutOption" {
		return "", false
	}
	return v.Elem().FieldByName("path").String(), true
}

func (m *modelRunner) PlanJSON(_ context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plans++
	state := ""
	for _, opt := range opts {
		if path, ok := optionPath(opt); ok {
			state = path
		}
	}
	rc := &tfjson.ResourceChange{
		Address: "aws_s3_bucket.a", Mode: tfjson.ManagedResourceMode, Type: "aws_s3_bucket",
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}},
	}
	summary := tfrun.ChangeSummary{Import: 1}
	if state != "" {
		m.cachedPlans++
		summary = tfrun.ChangeSummary{}
		if m.states[state] != m.providers() {
			rc.Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
			rc.Change.Before = map[string]any{"tags": map[string]any{"team": "a"}}
			rc.Change.After = map[string]any{"tags": map[string]any{}}
			summary.Change = 1
		}
	} else {
		rc.Change.Importing = &tfjson.Importing{ID: "a"}
	}
	m.last = &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{rc}}
	fmt.Fprintf(w, `{"@level":"info","type":"change_summary","changes":{"add":0,"change":%d,"import":%d,"remove":0,"operation":"plan"}}`+"\n", summary.Change, summary.Import)
	return true, nil
}

func (m *modelRunner) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last, nil
}

func (m *modelRunner) Apply(_ context.Context, opts ...tfexec.ApplyOption) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applies++
	for _, opt := range opts {
		if path, ok := optionPath(opt); ok {
			m.states[path] = m.providers()
		}
	}
	return nil
}

func (m *modelRunner) StateMv(context.Context, string, string, ...tfexec.StateMvCmdOption) error {
	return nil
}

func (m *modelRunner) StateRm(context.Context, string, ...tfexec.StateRmCmdOption) error {
	return nil
}

// providerRoot writes a root with one imported bucket and the provider
// blocks providers.
func providerRoot(t *testing.T, providers string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, rootfile.GeneratedFileName, "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}\n")
	writeConfig(t, dir, rootfile.ImportsFileName, "import {\n  to = aws_s3_bucket.a\n  id = \"a\"\n}\n")
	writeConfig(t, dir, "providers.tf", providers)
	return dir
}

// providerTidy is a tidying of a root, whose plans run against a snapshot if
// snapshot is set, or plain.
func providerTidy(t *testing.T, snapshot bool) (*tidying, *cloudModel, *tfrun.Cached) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir()) // snapshots go to the system's temporary directory
	dir := providerRoot(t, plainProviders)
	cloud := newCloudModel()
	root := &modelRunner{cloudModel: cloud, dir: dir}
	var cache *tfrun.Cached
	g := &generation{dir: dir, r: generating(Options{}), result: &Result{}, counting: &tfrun.Counting{Terraform: root}}
	if snapshot {
		g.opts = Options{ImportOnce: &ImportOnce{NewRunner: func(copied string) (tfrun.Terraform, error) {
			return &modelRunner{cloudModel: cloud, dir: copied}, nil
		}}}
		g.r = generating(g.opts)
		cache = cachedRunner(root, root, dir, g.opts)
		g.cache = cache
		g.followProviders()
		if err := g.buildSnapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cache.Release)
	}
	var tf tfrun.Terraform = root
	if cache != nil {
		tf = cache
	}
	ws := rootWorkspace(t, dir)
	d := tidier(tf, ws, Baseline{})
	shown, diags, err := d.plan()
	if err != nil || len(diags) > 0 {
		t.Fatalf("the first plan: %v, %v", err, diags)
	}
	d.baseline = newBaseline(shown, nil)
	return d, cloud, cache
}

// setProviders is an edit that has the root's provider blocks as providers.
func setProviders(d *tidying, providers string) *tidyEdit {
	return &tidyEdit{edit: plainEdit(func() (bool, error) {
		d.ws.Write("providers.tf", []byte(providers))
		return true, nil
	})}
}

// touch is an edit that changes nothing a plan shows.
func touch(d *tidying) *tidyEdit {
	return &tidyEdit{edit: plainEdit(func() (bool, error) {
		d.ws.Write(rootfile.GeneratedFileName, append(d.ws.files[rootfile.GeneratedFileName], '\n'))
		return true, nil
	})}
}

// A tidy pass that changes the provider's configuration, as moving the tags
// every resource shares into default_tags does, is judged by what a plan
// that imports shows, with and without the snapshot: the one that reads the
// state read before the change would show an update of the tags, and have
// the pass rejected only when the snapshot is used.
func TestProviderConfigEditSettlesAlikeWithTheSnapshot(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%v", snapshot), func(t *testing.T) {
			d, cloud, cache := providerTidy(t, snapshot)
			kept, err := d.settle(tidyPass{name: "providers", policy: Exact}, setProviders(d, taggedProviders))
			if err != nil {
				t.Fatal(err)
			}
			if !kept {
				t.Fatal("the edit of the provider block was rejected")
			}
			if got := string(d.ws.files["providers.tf"]); !strings.Contains(got, "default_tags") {
				t.Errorf("the provider block was undone:\n%s", got)
			}
			if !snapshot {
				return
			}
			if cloud.cachedPlans != 1 {
				t.Errorf("%d plans ran against the snapshot, want the first only", cloud.cachedPlans)
			}
			if !cache.Active() {
				t.Error("the snapshot was dropped")
			}
		})
	}
}

// After a provider block was edited, the next plan builds the snapshot of
// the configuration the root has, so that the plans after it are cached
// again; an edit that is undone costs the plan of it, and the snapshot
// before it serves again.
func TestSnapshotFollowsTheProviderConfiguration(t *testing.T) {
	d, cloud, cache := providerTidy(t, true)
	pass := tidyPass{name: "providers", policy: Exact}
	if kept, err := d.settle(pass, setProviders(d, taggedProviders)); err != nil || !kept {
		t.Fatalf("kept %v, %v", kept, err)
	}
	// The plan of the edit read the cloud as the user's plan does.
	cached, applies := cloud.cachedPlans, cloud.applies
	if cached != 1 || applies != 1 {
		t.Fatalf("after the provider edit: %d cached plans, %d snapshot builds, want 1 and 1", cached, applies)
	}
	// The next edit's plan builds the snapshot of the new configuration
	// and runs against it.
	if kept, err := d.settle(pass, touch(d)); err != nil || !kept {
		t.Fatalf("kept %v, %v", kept, err)
	}
	if cloud.applies != 2 || cloud.cachedPlans != 2 {
		t.Errorf("after the next edit: %d cached plans, %d snapshot builds, want 2 and 2", cloud.cachedPlans, cloud.applies)
	}
	// And the plans after it are cached without another build.
	if kept, err := d.settle(pass, touch(d)); err != nil || !kept {
		t.Fatalf("kept %v, %v", kept, err)
	}
	if cloud.applies != 2 || cloud.cachedPlans != 3 {
		t.Errorf("after the third edit: %d cached plans, %d snapshot builds, want 3 and 2", cloud.cachedPlans, cloud.applies)
	}
	if !cache.Active() {
		t.Error("the snapshot was dropped")
	}
}

// An edit of the provider blocks that a plan rejects leaves the plans after
// it cached, by the snapshot as it was.
func TestRejectedProviderEditKeepsTheSnapshot(t *testing.T) {
	d, cloud, _ := providerTidy(t, true)
	rejected := &tidyEdit{edit: plainEdit(func() (bool, error) {
		d.ws.Write("providers.tf", []byte(taggedProviders))
		// A change the baseline doesn't have.
		d.ws.Write(rootfile.GeneratedFileName, []byte("resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}\n\nresource \"aws_s3_bucket\" \"b\" {\n  bucket = \"b\"\n}\n"))
		return true, nil
	}), review: func(*tidyAttempt, tidyReview) (tidyDecision, error) {
		return tidyDecision{outcome: tidyReject}, nil
	}}
	if kept, err := d.settle(tidyPass{name: "providers", policy: Exact}, rejected); err != nil || kept {
		t.Fatalf("kept %v, %v", kept, err)
	}
	before := cloud.cachedPlans
	if kept, err := d.settle(tidyPass{name: "touch", policy: Exact}, touch(d)); err != nil || !kept {
		t.Fatalf("kept %v, %v", kept, err)
	}
	if cloud.cachedPlans != before+1 || cloud.applies != 1 {
		t.Errorf("the plan after the undone edit: %d cached plans (from %d), %d builds, want it cached, one build", cloud.cachedPlans, before, cloud.applies)
	}
}

func TestProviderConfigFingerprint(t *testing.T) {
	t.Parallel()
	dir := providerRoot(t, plainProviders)
	of := func() string {
		t.Helper()
		got, err := providerConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	base := of()
	// Other blocks, formatting and the file a block is in don't count.
	writeConfig(t, dir, rootfile.GeneratedFileName, "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"other\"\n}\n")
	writeConfig(t, dir, "providers.tf", "provider \"aws\" {\n\n      region   =   \"us-east-1\"\n}\n\n")
	if of() != base {
		t.Error("an edit that isn't of a provider block changed the fingerprint")
	}
	// A provider block, its default_tags or an alias do.
	for name, providers := range map[string]string{
		"default_tags": taggedProviders,
		"alias":        plainProviders + "\nprovider \"aws\" {\n  alias  = \"other\"\n  region = \"eu-west-1\"\n}\n",
		"ignore_tags":  "provider \"aws\" {\n  region = \"us-east-1\"\n  ignore_tags {\n    keys = [\"x\"]\n  }\n}\n",
	} {
		writeConfig(t, dir, "providers.tf", providers)
		if of() == base {
			t.Errorf("%s didn't change the fingerprint", name)
		}
	}
}

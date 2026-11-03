// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// importRecorder is a runner that counts the plans that import: those a
// state snapshot saves.
type importRecorder struct {
	tfrun.StateTerraform
	*planCounts
}

// planCounts count the plans of a generation, those of the copy a snapshot
// is built in included.
type planCounts struct {
	mu      sync.Mutex
	plans   int
	imports int // plans that imported at least one resource
	applies int
}

func (r *importRecorder) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	var out bytes.Buffer
	changed, err := r.StateTerraform.PlanJSON(ctx, &out, opts...)
	_, _ = w.Write(out.Bytes())
	imported := false
	for _, line := range bytes.Split(out.Bytes(), []byte("\n")) {
		var msg struct {
			Type    string `json:"type"`
			Changes struct {
				Import int `json:"import"`
			} `json:"changes"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.Type == "change_summary" && msg.Changes.Import > 0 {
			imported = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plans++
	if imported {
		r.imports++
	}
	return changed, err
}

func (r *importRecorder) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	r.mu.Lock()
	r.applies++
	r.mu.Unlock()
	return r.StateTerraform.Apply(ctx, opts...)
}

// TestGenerateWithSnapshot generates the same root of the random provider
// with and without import-once state, and checks that the files are the
// same, that the snapshot saves plans that import, and that building it
// calls no provider write (no ApplyResourceChange). It downloads Terraform
// and the provider, so it runs in CI:
//
//	go test -tags integration -run TestGenerateWithSnapshot ./internal/engine/
func TestGenerateWithSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()

	execPath, err := tfrun.TerraformBinary.Find(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}
	tfVersion, err := tfrun.BinaryVersion(ctx, execPath)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := LatestProviderVersion(ctx, http.DefaultClient, "", "hashicorp/random")
	if err != nil {
		t.Fatal(err)
	}
	p := rootfile.Provider{Name: "random", Source: "hashicorp/random", Version: rootfile.ProviderConstraint(latest)}
	providers, err := rootfile.ProvidersFile(p)
	if err != nil {
		t.Fatal(err)
	}
	config := map[string][]byte{rootfile.VersionsFileName: rootfile.VersionsFile(rootfile.RequiredVersion(tfVersion), p), rootfile.ProvidersFileName: providers}
	imports := []rootfile.Import{
		{Type: "random_string", Name: "first", ID: "s3cr3tvalue"},
		{Type: "random_string", Name: "second", ID: "an0therValue"},
		{Type: "random_string", Name: "fifth", ID: "thirdValue12"},
		{Type: "random_string", Name: "sixth", ID: "fourthValue1"},
		{Type: "random_integer", Name: "third", ID: "5,1,10"},
		{Type: "random_uuid", Name: "fourth", ID: "6c1f6e5e-9a37-4b69-8d6a-3a7c2f1d9e10"},
	}

	generate := func(snapshot bool) (map[string]string, [2]*planCounts, string) {
		dir := t.TempDir()
		runner, err := tfrun.New(dir, execPath, filepath.Join(cache, "plugins"), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		counts, copyCounts := &planCounts{}, &planCounts{}
		rec := &importRecorder{StateTerraform: runner, planCounts: counts}
		opts := Options{Config: config, Defaults: map[string][]string{"random_string": {"special=true", "upper=true", "lower=true", "numeric=true", "min_lower=0", "min_upper=0", "min_numeric=0", "min_special=0"}}}
		copyLog := filepath.Join(t.TempDir(), "copy.log")
		if snapshot {
			opts.ImportOnce = &ImportOnce{NewRunner: func(copied string) (tfrun.Terraform, error) {
				cr, err := tfrun.New(copied, execPath, filepath.Join(cache, "plugins"), nil, nil)
				if err != nil {
					return nil, err
				}
				// The provider's calls, to show the apply writes nothing.
				if err := cr.SetLogPath(copyLog); err != nil {
					return nil, err
				}
				if err := cr.SetLog("TRACE"); err != nil {
					return nil, err
				}
				return &importRecorder{StateTerraform: cr, planCounts: copyCounts}, nil
			}}
		}
		result, err := Generate(ctx, rec, dir, imports, opts)
		if err != nil {
			t.Fatalf("snapshot=%v: %v", snapshot, err)
		}
		t.Logf("snapshot=%v plans by step: %+v", snapshot, result.Plans)
		if snapshot && !result.UsedSnapshot {
			t.Error("the plans didn't run against a snapshot")
		}
		if !snapshot && result.UsedSnapshot {
			t.Error("a snapshot was used without the option")
		}
		for _, c := range result.Gate {
			if !c.Passed {
				t.Errorf("snapshot=%v: gate check %s failed: %v", snapshot, c.Name, c.Details)
			}
		}
		files := map[string]string{}
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			if d.IsDir() && rel == ".terraform" {
				return filepath.SkipDir
			}
			if d.Type().IsRegular() {
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[rel] = string(content)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files, [2]*planCounts{counts, copyCounts}, copyLog
	}

	plain, plainRec, _ := generate(false)
	cached, cachedRec, copyLog := generate(true)

	if len(plain) != len(cached) {
		t.Errorf("the files differ: %d without the snapshot, %d with it", len(plain), len(cached))
	}
	for name, content := range plain {
		if got, ok := cached[name]; !ok {
			t.Errorf("%s is missing with the snapshot", name)
		} else if got != content {
			t.Errorf("%s differs with the snapshot:\n--- without\n%s\n--- with\n%s", name, content, got)
		}
	}
	root, copied := cachedRec[0], cachedRec[1]
	t.Logf("plans that import: %d of %d without the snapshot; with it, %d of %d in the root and %d in the copy the snapshot is built in", plainRec[0].imports, plainRec[0].plans, root.imports, root.plans, copied.imports)
	if root.imports >= plainRec[0].imports {
		t.Errorf("the snapshot saved no plans that import in the root: %d, from %d", root.imports, plainRec[0].imports)
	}
	if root.applies != 0 || copied.applies != 1 {
		t.Errorf("want one apply, of the snapshot's import-only plan in the copy, got %d in the root and %d in the copy", root.applies, copied.applies)
	}
	log, err := os.ReadFile(copyLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "ImportResourceState") {
		t.Error("the copy's log shows no import")
	}
	if strings.Contains(string(log), "ApplyResourceChange") {
		t.Error("building the snapshot called ApplyResourceChange")
	}
	// No snapshot is left behind.
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), tfrun.SnapshotPattern+"*"))
	if len(left) > 0 {
		t.Errorf("snapshots left behind: %v", left)
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// What the sandbox tests of every cloud share: they create an estate of
// testdata/sandbox in a real account as one identity, import it as
// another, read-only, and destroy it.

// sandboxEstate is an estate of testdata/sandbox in a directory, applied
// with vars, such as its suffix, which tells its names apart from other
// runs'.
type sandboxEstate struct {
	tf   *engine.Runner
	vars []string
}

// seedSandbox writes testdata/sandbox/<name> into dir and initialises it,
// to apply with vars and env, the fixture identity's credentials, on top
// of the process's environment. The test's cleanup destroys what it
// creates.
func seedSandbox(ctx context.Context, t *testing.T, name, dir, execPath, plugins string, env map[string]string, vars ...string) *sandboxEstate {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "sandbox", name, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := engine.NewTerraform(dir, execPath, plugins, env)
	if err != nil {
		t.Fatal(err)
	}
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	t.Cleanup(func() {
		// Its own deadline: the test's may have passed.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		opts := make([]tfexec.DestroyOption, 0, len(vars))
		for _, v := range vars {
			opts = append(opts, tfexec.Var(v))
		}
		if err := tf.Destroy(ctx, opts...); err != nil {
			t.Errorf("destroy the sandbox estate in %s: %v", dir, err)
		}
	})
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("seed: init: %v", err)
	}
	return &sandboxEstate{tf: tf, vars: vars}
}

// apply applies the estate with its vars and opts, such as more variables
// or targets, and returns the resources it then has.
func (e *sandboxEstate) apply(ctx context.Context, t *testing.T, opts ...tfexec.ApplyOption) []*tfjson.StateResource {
	t.Helper()
	for _, v := range e.vars {
		opts = append(opts, tfexec.Var(v))
	}
	if err := e.tf.Apply(ctx, opts...); err != nil {
		t.Fatalf("seed: apply: %v", err)
	}
	state, err := e.tf.Show(ctx)
	if err != nil {
		t.Fatalf("seed: show: %v", err)
	}
	var created []*tfjson.StateResource
	for _, r := range state.Values.RootModule.Resources {
		if r.Mode == tfjson.ManagedResourceMode {
			created = append(created, r)
		}
	}
	return created
}

// readCoverage returns the report of the import in out.
func readCoverage(t *testing.T, out string) report.Report {
	t.Helper()
	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	return coverage
}

// envOr returns the environment variable name, or fallback if it is unset.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// checkNoAuthorizationErrors checks that neither the import's log nor its
// report says a call was denied: that none of their lines holds one of
// denials, what the cloud says when the caller's role doesn't allow a
// call.
func checkNoAuthorizationErrors(t *testing.T, logged string, coverage report.Report, denials []string) {
	t.Helper()
	reported, err := json.Marshal(coverage)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []struct{ where, content string }{{"the import's log", logged}, {"coverage.json", string(reported)}} {
		for _, line := range strings.Split(text.content, "\n") {
			for _, denied := range denials {
				if strings.Contains(line, denied) {
					t.Errorf("%s: a call was denied (%s): %s", text.where, denied, line)
					break
				}
			}
		}
	}
}

// syncBuffer is a log writer that keeps what is written, for the listers'
// goroutines to write to at once.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

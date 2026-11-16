// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// TestScopesShareDecodedCheckpoints checks that the indexes of a
// command's scopes decode a checkpoint once while it is the same, and
// again once a scope before them rewrote it, and that a missing or broken
// checkpoint reads as it does without the cache.
func TestScopesShareDecodedCheckpoints(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "sqs")
	first := &engine.Result{Imported: []rootfile.Import{{Type: "aws_sqs_queue", Name: "a", ID: "a"}}}
	if err := saveCheckpoint(out, dir, "fp", first); err != nil {
		t.Fatal(err)
	}
	r := NewRun()
	read := func() *checkpoint {
		t.Helper()
		r.openOutput(out)
		c, err := r.outputIndex(out).root(dir).checkpoint()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := read(), read()
	if a == nil || a != b {
		t.Fatalf("two scopes decoded the same checkpoint apart: %p, %p", a, b)
	}

	second := &engine.Result{Imported: []rootfile.Import{{Type: "aws_sqs_queue", Name: "b", ID: "b"}}}
	if err := saveCheckpoint(out, dir, "fp", second); err != nil {
		t.Fatal(err)
	}
	c := read()
	if c == a || len(c.Result.Imported) != 1 || c.Result.Imported[0].ID != "b" {
		t.Fatalf("a rewritten checkpoint reads as before: %+v", c.Result)
	}

	if c, err := newCachedDiskIndex(out, &r.shared.checkpoints).root(filepath.Join(out, "aws", "none")).checkpoint(); err != nil || c != nil {
		t.Errorf("a missing checkpoint: %v, %v", c, err)
	}
	path := rootCheckpoints.path(out, dir)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	cached, err := newCachedDiskIndex(out, &r.shared.checkpoints).root(dir).checkpoint()
	_, plain := newDiskIndex(out).root(dir).checkpoint()
	if cached != nil || err == nil || plain == nil || err.Error() != plain.Error() || !strings.Contains(err.Error(), path) {
		t.Errorf("a broken checkpoint: %v, %v; without the cache: %v", cached, err, plain)
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// writeGeneratedRoots writes roots roots of resources resources each, as
// earlier imports leave them: a .tf file and a checkpoint whose result
// records what the root imports.
func writeGeneratedRoots(tb testing.TB, roots, resources int) string {
	tb.Helper()
	out := tb.TempDir()
	for d := range roots {
		dir := filepath.Join(out, fmt.Sprintf("service_%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("# root\n"), 0o644); err != nil {
			tb.Fatal(err)
		}
		result := &engine.Result{}
		for i := range resources {
			id := fmt.Sprintf("queue-%03d-%04d", d, i)
			name := fmt.Sprintf("q_%d", i)
			result.Imported = append(result.Imported, rootfile.Import{Type: "aws_sqs_queue", Name: name, ID: id})
			result.Addresses = append(result.Addresses, engine.Managed{Type: "aws_sqs_queue", ID: id, Address: "aws_sqs_queue." + name})
		}
		if err := saveCheckpoint(out, dir, "fp", result); err != nil {
			tb.Fatal(err)
		}
	}
	return out
}

// BenchmarkScopesReadOutput is what four scopes of a command read of an
// output of 200 roots of 500 resources: each opens the run's index (see
// Run.openOutput) and asks which root holds each resource.
func BenchmarkScopesReadOutput(b *testing.B) {
	out := writeGeneratedRoots(b, 200, 500)
	b.ReportAllocs()
	for b.Loop() {
		r := NewRun()
		for range 4 {
			r.openOutput(out)
			held, err := r.outputIndex(out).heldElsewhere(nil)
			if err != nil {
				b.Fatal(err)
			}
			if len(held) != 200*500 {
				b.Fatalf("%d held", len(held))
			}
		}
	}
}

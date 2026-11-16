// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// writeLargeOut writes an output directory of n imported resources over
// ten scopes, each with a saved inventory that names and tags them.
func writeLargeOut(tb testing.TB, n int) string {
	tb.Helper()
	out := tb.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Engine = report.Component{Name: "terraform", Version: "1.17.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws", Constraint: "~> 6.0"}
	const scopes = 10
	records := make([][]map[string]any, scopes)
	for s := range scopes {
		sc := fmt.Sprintf("aws/1234567890%02d/eu-west-2", s)
		d := report.Directory{Path: sc, Scope: sc}
		for i := range n / scopes {
			id := fmt.Sprintf("queue-%d-%d", s, i)
			d.Imported = append(d.Imported, report.Resource{Address: fmt.Sprintf("aws_sqs_queue.q_%d", i), ID: id})
			records[s] = append(records[s], map[string]any{
				"service": "sqs", "provider": "aws", "type": "aws_sqs_queue", "id": id, "name": id,
				"tags":       map[string]string{"team": "payments", "env": "prod"},
				"attributes": map[string]string{"visibility_timeout_seconds": "30"},
			})
		}
		r.Directories = append(r.Directories, d)
		r.AddDiscovered(sc, n/scopes)
	}
	r.Finish(map[string]int{"aws_sqs_queue": n}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		tb.Fatal(err)
	}
	dir := filepath.Join(out, inventoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	for s, recs := range records {
		content, err := json.Marshal(map[string]any{
			"version": 2, "provider": "aws", "scope": fmt.Sprintf("aws/1234567890%02d/eu-west-2", s), "services": []string{"sqs"}, "records": recs,
		})
		if err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("aws-%02d.json", s)), content, 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return out
}

// BenchmarkBuildInventory builds the inventory document of 100,000
// resources.
func BenchmarkBuildInventory(b *testing.B) {
	out := writeLargeOut(b, 100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := Build(Options{Out: out, Document: DocInventory, Now: fixedNow}); err != nil {
			b.Fatal(err)
		}
	}
}

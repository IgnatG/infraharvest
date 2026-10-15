// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package repocheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitignoreChangedRules(t *testing.T) {
	repo := newRepositoryFixture(t)
	ignore, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	repo.write(".gitignore", string(ignore))

	for _, tt := range []struct {
		path    string
		ignored bool
	}{
		{"infraharvest/aws/main.tf", true},
		{"internal/infraharvest/fixture.go", false},
		{"infraharvest.go", false},
		{"site/index.html", true},
		{"docs/site/example.md", false},
		{"site.md", false},
		{"cpu.pprof", true},
		{"profiles/heap.pprof", true},
		{"profiles/heap.pprof.txt", false},
		{"cmd/tmp/fixture.go", false},
		{"init.tf", false},
		{"examples/init.tf", false},
	} {
		// The fixture fails the test through the outer t, so the cases run in
		// turn rather than as subtests; a failure names the path it checked.
		status := 1
		if tt.ignored {
			status = 0
		}
		repo.git(status, "check-ignore", "--quiet", "--no-index", "--", tt.path)
	}
}

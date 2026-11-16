// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package repocheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const changesScript = ".github/scripts/changes.sh"

// TestChangesClouds checks which jobs of the tests workflow a pull request
// runs: core code that one cloud's build alone reads runs that cloud's e2e
// jobs alone, other core code runs every cloud's, and documentation runs
// none.
func TestChangesClouds(t *testing.T) {
	for _, tt := range []struct {
		name    string
		changed map[string]string
		want    string
	}{
		{
			name:    "an AWS lister",
			changed: map[string]string{"internal/providers/aws/sqs_queues.go": "package aws\n"},
			want:    "code=true\npermissions=false\naws=true\ngoogle=false\nazure=false\n",
		},
		{
			name: "Azure's catalogue data and command",
			changed: map[string]string{
				"internal/catalogue/data/azurerm/network.yaml": "x: 1\n",
				"internal/cli/providers/provider_cmd_azure.go": "package providers\n",
			},
			want: "code=true\npermissions=false\naws=false\ngoogle=false\nazure=true\n",
		},
		{
			name: "a Google e2e test and a store",
			changed: map[string]string{
				"e2e/drift_google_test.go":           "//go:build e2e && (!minimal || google)\n\npackage e2e\n",
				"internal/managed/stores/gcs/gcs.go": "package gcs\n",
			},
			want: "code=true\npermissions=false\naws=false\ngoogle=true\nazure=false\n",
		},
		{
			name: "an e2e helper every cloud builds",
			changed: map[string]string{
				"e2e/harness_test.go": "//go:build e2e\n\npackage e2e\n",
			},
			want: "code=true\npermissions=false\naws=true\ngoogle=true\nazure=true\n",
		},
		{
			name: "the engine with an AWS lister",
			changed: map[string]string{
				"internal/engine/engine.go":            "package engine\n",
				"internal/providers/aws/sqs_queues.go": "package aws\n",
			},
			want: "code=true\npermissions=false\naws=true\ngoogle=true\nazure=true\n",
		},
		{
			name:    "documentation",
			changed: map[string]string{"docs/usage.md": "# Usage\n"},
			want:    "code=false\npermissions=false\naws=false\ngoogle=false\nazure=false\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepositoryFixture(t)
			repo.install(changesScript)
			repo.track("README.md", "# Fixture\n")
			repo.git(0, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "base")
			base := strings.TrimSpace(repo.commandAt(repo.dir, 0, "git", "rev-parse", "HEAD"))
			for path, content := range tt.changed {
				repo.track(path, content)
			}
			repo.git(0, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "change")
			head := strings.TrimSpace(repo.commandAt(repo.dir, 0, "git", "rev-parse", "HEAD"))

			output := filepath.Join(t.TempDir(), "output")
			env := repo.env
			repo.env = append(append([]string(nil), env...), "BASE="+base, "HEAD="+head, "GITHUB_OUTPUT="+output)
			repo.commandAt(repo.dir, 0, "bash", filepath.Join(repo.dir, filepath.FromSlash(changesScript)))
			repo.env = env
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("outputs:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

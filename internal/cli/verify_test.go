// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --config applies the configuration file's settings verify has, as drift
// does, and leaves the import's others, such as services, to it.
func TestVerifyConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		path := filepath.Join(dir, "infraharvest.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, tc := range map[string]struct{ config, want string }{
		"the engine applies": {"version: 1\nsettings: { engine: pulumi, services: [vpc] }\n", `unknown --engine "pulumi"`},
		// Without conftest on PATH, --policy fails for that; with it, for
		// the directory: either way, the setting applied.
		"the policies apply":       {"version: 1\nsettings: { policy: [no-such-policies] }\n", "polic"},
		"a provider's engine":      {"version: 1\nproviders: { aws: { engine: tofu } }\n", "under settings"},
		"the command line decides": {"version: 1\nsettings: { engine: pulumi }\n", "no generated roots"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newVerifyCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			args := []string{"--out", t.TempDir(), "--config", write(tc.config)}
			if name == "the command line decides" {
				args = append(args, "--engine=terraform")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %s", err, tc.want)
			}
		})
	}
}

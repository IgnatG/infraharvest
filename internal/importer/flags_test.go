// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// --policy is an explicit request: without conftest it fails up front,
// rather than leaving the check out as an uninstalled scanner is, and so
// does a policy directory that isn't one. Those that are come back
// absolute, as conftest runs in each root it checks.
func TestResolvePolicies(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	if err := os.Mkdir("policy", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("file.rego", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	withConftest, without := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(withConftest, engine.ConftestBinary), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withConftest, engine.ConftestBinary+".exe"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		path    string
		dirs    []string
		want    []string
		wantErr string
	}{
		"none, without conftest":   {path: without},
		"a directory":              {path: withConftest, dirs: []string{"policy"}, want: []string{filepath.Join(work, "policy")}},
		"conftest isn't installed": {path: without, dirs: []string{"policy"}, wantErr: "--policy: checking the roots against policies needs conftest on PATH"},
		"a file":                   {path: withConftest, dirs: []string{"file.rego"}, wantErr: "--policy: file.rego isn't a directory"},
		"missing":                  {path: withConftest, dirs: []string{"nowhere"}, wantErr: "--policy:"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			got, err := ResolvePolicies(tc.dirs)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("got %v, %v, want an error with %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

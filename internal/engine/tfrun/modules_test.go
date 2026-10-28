// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A root whose init kept its working files elsewhere, its TF_DATA_DIR,
// as drift's of roots that may come from someone else does, has its
// modules read from there: a downloaded module by the absolute path
// Terraform lists it at, a local one relative to the root. The root's own
// .terraform isn't read.
func TestInstalledModules(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vpc := filepath.Join(data, "modules", "vpc")
	write(filepath.Join(data, "modules", "modules.json"), fmt.Sprintf(`{"Modules":[{"Key":"","Dir":"."},{"Key":"vpc","Dir":%q},{"Key":"queue","Dir":"../../modules/queue"}]}`, filepath.ToSlash(vpc)))
	write(filepath.Join(dir, ".terraform", "modules", "modules.json"), `{"Modules":[{"Key":"planted","Dir":".terraform/modules/planted"}]}`)

	got, err := InstalledModules(dir, data)

	want := []InstalledModule{{Key: "vpc", Dir: vpc}, {Key: "queue", Dir: filepath.Join(dir, "..", "..", "modules", "queue")}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %v, want %v", got, err, want)
	}
	if got, err := InstalledModules(dir, ""); err != nil || len(got) != 1 || got[0].Key != "planted" {
		t.Errorf("without a data directory: got %v, %v, want the root's .terraform", got, err)
	}
}

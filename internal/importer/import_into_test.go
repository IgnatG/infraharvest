// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// importInto in discover mode runs the whole listing, as a command does:
// the provider's Init, the catalogue, the listers, the default selection,
// the managed state and owned resources, and the run then writes the
// selection file and the inventory, and fails as incomplete for a service
// that listed in part.
func TestImportIntoDiscovers(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "terraform.tfstate")
	content := `{"version": 4, "resources": [{"mode": "managed", "type": "fake_thing", "instances": [{"attributes": {"id": "id-2"}}]}]}`
	if err := os.WriteFile(state, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	options := Options{
		Discover:     true,
		Out:          filepath.Join(dir, "out"),
		Selection:    filepath.Join(dir, "selection.yaml"),
		Services:     []string{"good", "partial", "skipped"},
		Skip:         []string{"skipped"},
		ManagedState: []string{state},
		ListParallel: 1,
	}
	run := NewRun()
	err := run.finish(importInto(t.Context(), run, &fakeProvider{}, options, []string{"eu-west-2"}))
	if ExitCode(err) != report.ExitIncomplete || !strings.Contains(err.Error(), "service partial") {
		t.Fatalf("want the run incomplete for the partial service, got %v", err)
	}

	f, err := selection.Load(options.Selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Resources) != 2 {
		t.Fatalf("resources: %+v", f.Resources)
	}
	for _, r := range f.Resources {
		switch r.ID {
		case "id-1":
			if !r.Include || r.Service != "good" {
				t.Errorf("listed resource: %+v", r)
			}
		case "id-2":
			if r.Include || !strings.HasPrefix(r.Reason, report.TerraformPrefix) || r.Service != "partial" {
				t.Errorf("managed resource: %+v", r)
			}
		default:
			t.Errorf("unexpected entry %+v", r)
		}
	}
	if _, err := os.Stat(inventoryPath(options.Out, "fake", []string{"eu-west-2"})); err != nil {
		t.Errorf("no inventory saved: %v", err)
	}
	if !run.covered[coverKey("", "good")] || run.covered[coverKey("", "skipped")] {
		t.Errorf("covered %v", run.covered)
	}
}

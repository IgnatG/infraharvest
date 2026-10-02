package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestExportPlanFileIsOwnerOnlyAndLoadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "generated", "aws")
	plan := &ImportPlan{
		Provider:         "aws",
		Options:          ImportOptions{Resources: []string{"sqs"}},
		ImportedResource: map[string][]terraformutils.Resource{},
	}

	if err := ExportPlanFile(plan, dir, "plan.json"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "plan.json")
	loaded, err := LoadPlanfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider != "aws" || len(loaded.Options.Resources) != 1 {
		t.Errorf("plan did not round-trip: %+v", loaded)
	}

	if runtime.GOOS == "windows" {
		return
	}
	for p, wantNoBits := range map[string]os.FileMode{
		path: 0o077, // plan holds resource attributes: owner only
		dir:  0o022, // directories: not writable by others
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&wantNoBits != 0 {
			t.Errorf("mode of %s: got %#o, want none of %#o", p, perm, wantNoBits)
		}
	}
}

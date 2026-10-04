// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/adapters"
	"github.com/IgnatG/infraharvest/adapters/moduleinterface"
)

// TestAdaptersMatchModuleInterfaces checks every adapter against the
// interface of the module version it pins, from testdata/interfaces (see
// adapters/cmd/adaptercheck -write). Pinning a new version needs that
// version's interface.
func TestAdaptersMatchModuleInterfaces(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "interfaces", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]*moduleinterface.Interface{}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var iface moduleinterface.Interface
		if err := json.Unmarshal(content, &iface); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		pinned[iface.Source+" "+iface.Version] = &iface
	}
	for _, provider := range adapters.Providers() {
		for _, a := range adapters.For(provider) {
			iface, ok := pinned[a.Source+" "+a.Version]
			if !ok {
				t.Errorf("%s %s: no interface in testdata/interfaces", a.Source, a.Version)
				continue
			}
			for _, problem := range moduleinterface.Check(a, iface) {
				t.Errorf("%s %s: %s", a.Source, a.Version, problem)
			}
		}
	}
}

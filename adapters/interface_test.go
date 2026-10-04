// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// moduleInterface is a module version's variables and outputs, taken from
// its source. testdata/interfaces has one per adapter.
type moduleInterface struct {
	Source    string `json:"source"`
	Version   string `json:"version"`
	Variables map[string]struct {
		Required bool `json:"required"`
	} `json:"variables"`
	Outputs []string `json:"outputs"`
}

func loadInterfaces(t *testing.T) []moduleInterface {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "interfaces", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var interfaces []moduleInterface
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var m moduleInterface
		if err := json.Unmarshal(content, &m); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		interfaces = append(interfaces, m)
	}
	return interfaces
}

// TestAdaptersMatchModuleInterfaces checks every adapter against the
// interface of the module version it pins: every argument it can set is a
// variable, every required variable is one it sets, and every output it
// names exists. Pinning a new version needs that version's interface.
func TestAdaptersMatchModuleInterfaces(t *testing.T) {
	interfaces := loadInterfaces(t)
	for _, provider := range []string{"aws"} {
		for _, a := range For(provider) {
			i := slices.IndexFunc(interfaces, func(m moduleInterface) bool { return m.Source == a.Source && m.Version == a.Version })
			if i < 0 {
				t.Errorf("%s %s: no interface in testdata/interfaces", a.Source, a.Version)
				continue
			}
			m := interfaces[i]
			for _, input := range a.Inputs {
				if _, ok := m.Variables[input]; !ok {
					t.Errorf("%s %s has no variable %q", a.Source, a.Version, input)
				}
			}
			for name, v := range m.Variables {
				if v.Required && !slices.Contains(a.Inputs, name) {
					t.Errorf("%s %s: the adapter doesn't set the required variable %q", a.Source, a.Version, name)
				}
			}
			for _, output := range a.Outputs {
				if !slices.Contains(m.Outputs, output) {
					t.Errorf("%s %s has no output %q", a.Source, a.Version, output)
				}
			}
		}
	}
}

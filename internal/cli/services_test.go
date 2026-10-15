// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// infraharvest services <provider> prints the provider's services, one
// per line.
func TestServicesCommand(t *testing.T) {
	for _, name := range providers.Names() {
		var out bytes.Buffer
		root := NewCmdRoot()
		root.SetOut(&out)
		root.SetArgs([]string{"services", name})
		if err := root.Execute(); err != nil {
			t.Fatalf("services %s: %v", name, err)
		}
		got := strings.Fields(out.String())
		if want := importer.Services(providers.New(name)); len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("services %s: got %v, want %v", name, got, want)
		}
	}

	root := NewCmdRoot()
	root.SetArgs([]string{"services", "nowhere"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), `unknown provider "nowhere"`) {
		t.Errorf("services nowhere: got %v, want an unknown provider", err)
	}
}

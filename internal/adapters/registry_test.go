// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
)

// The adapters are keyed by provider command, as the import looks them up
// (see harvest.Profile.Command), not by Terraform provider, which google
// --beta and azure don't share with their commands.
func TestProvidersAreCommands(t *testing.T) {
	t.Parallel()
	for _, name := range Providers() {
		if !slices.Contains(config.ProviderCommands, name) {
			t.Errorf("adapters for %q, not a provider command (%v)", name, config.ProviderCommands)
		}
	}
}

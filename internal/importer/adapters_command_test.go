// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// betaProvider is google --beta: its Terraform provider is google-beta,
// its command google.
type betaProvider struct{ scopedProvider }

func (betaProvider) Name() string { return "google-beta" }
func (betaProvider) Profile() harvest.Profile {
	return harvest.Profile{Command: "google"}
}

// The curated modules are looked up by the provider's command, as the
// import looks them up, so that google --beta gets google's and azure,
// whose Terraform provider is azurerm, gets its own.
func TestAdaptersByCommand(t *testing.T) {
	for _, provider := range []harvest.Provider{betaProvider{}, directoryProvider{}} {
		if len(adapters.For(providerDir(provider))) == 0 {
			t.Errorf("%s: no curated modules", provider.Name())
		}
	}
}

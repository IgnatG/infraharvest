// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providers

import (
	"context"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/managed"
)

// A backend whose provider isn't compiled in fails, naming the backend and
// the build tag that adds it, instead of reading nothing.
func TestManagedStoresNotCompiledIn(t *testing.T) {
	saved := compiledStores
	t.Cleanup(func() { compiledStores = saved })
	compiledStores = managed.Stores{}

	for source, want := range map[string][]string{
		"s3://acme-state/imported/":                         {"s3 backend", "-tags minimal,aws"},
		"gs://acme-state/imported/":                         {"gcs backend", "-tags minimal,google"},
		"https://acmestate.blob.core.windows.net/tfstate/a": {"azurerm backend", "-tags minimal,azure"},
	} {
		_, err := managed.Load(context.Background(), []string{source}, managedStores())
		if err == nil {
			t.Errorf("%s: want an error", source)
			continue
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: the error doesn't say %q: %v", source, w, err)
			}
		}
	}
	if managedStores().HCPTerraform == nil {
		t.Error("HCP Terraform's store is in every build")
	}
}

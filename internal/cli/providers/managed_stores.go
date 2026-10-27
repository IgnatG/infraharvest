// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providers

import (
	"context"
	"fmt"

	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/managed/stores/hcp"
)

// compiledStores are the stores of the providers compiled in: each
// provider's definition sets the store of the backend its cloud keeps state
// in (see definition.stores), which links that cloud's SDK.
var compiledStores managed.Stores

// managedStores returns the stores the provider commands read
// --managed-state with: those compiled in, HCP Terraform's, which is
// plain HTTP and in every build, and, for a backend whose provider isn't
// compiled in, one that fails, naming the backend and the build tag that
// adds it, rather than reading nothing.
func managedStores() managed.Stores {
	stores := compiledStores
	stores.HCPTerraform = hcp.New
	if stores.S3 == nil {
		stores.S3 = func(context.Context, string, string) (managed.ObjectStore, error) {
			return nil, notCompiledIn("s3", "aws")
		}
	}
	if stores.GCS == nil {
		stores.GCS = func(context.Context) (managed.ObjectStore, error) {
			return nil, notCompiledIn("gcs", "google")
		}
	}
	if stores.AzureBlob == nil {
		stores.AzureBlob = func(context.Context, string) (managed.ObjectStore, error) {
			return nil, notCompiledIn("azurerm", "azure")
		}
	}
	return stores
}

// notCompiledIn is the error of reading the state of backend in a build
// without the provider whose build tag is tag.
func notCompiledIn(backend, tag string) error {
	return fmt.Errorf("reading the %s backend's state needs the %s provider, which this infraharvest was built without: build it with -tags minimal,%s, or without minimal", backend, tag, tag)
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"strings"
)

// resourceProviders returns the providers of configured, by local name,
// that the types of imports imply, such as azapi for an azapi_resource,
// which an Azure import brings in for what azurerm has no type for: a
// root that imports such resources requires and configures them, as one
// that calls a module using them does (see requireProviders), and pins
// each to the release init selects (see pinModuleProviders). An import
// that names its provider, such as google-beta, is the root's own
// provider's.
func resourceProviders(imports []Import, configured map[string]Provider) []string {
	var names []string
	for _, imp := range imports {
		if imp.Provider != "" {
			continue
		}
		implied, _, _ := strings.Cut(imp.Type, "_")
		if _, ok := configured[implied]; ok && !slices.Contains(names, implied) {
			names = append(names, implied)
		}
	}
	return names
}

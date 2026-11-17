// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import "fmt"

// ProviderSettings are what one Import call initialises its provider with
// (see Provider.Init): a struct of the provider's own, such as aws.Settings,
// which names where the call lists and who it lists as. Each provider
// takes its own type and refuses another's (see SettingsOf).
type ProviderSettings interface {
	// InventoryKey returns the settings as the ordered strings that name
	// the call's inventory (see --reuse-inventory), which the importer
	// hashes. Settings that are the same have the same key, and a key is
	// never changed for settings that were there before: an inventory
	// that an earlier release saved is found by it.
	InventoryKey() []string
}

// SettingsOf returns settings as the provider's own type S. nil, a call
// that sets nothing, is S's zero value; settings of another type is an
// error that names the provider.
func SettingsOf[S ProviderSettings](provider string, settings ProviderSettings) (S, error) {
	var zero S
	switch s := settings.(type) {
	case nil:
		return zero, nil
	case S:
		return s, nil
	default:
		return zero, fmt.Errorf("%s: initialised with %T, want %T", provider, settings, zero)
	}
}

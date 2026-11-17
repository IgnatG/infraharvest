// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"os"
	"path/filepath"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// observedRoot is what a root's own files say of where it has its
// resources, which a checkpoint without Addresses (an older release's)
// doesn't: no cloud call, and no Terraform command, reads it.
type observedRoot struct {
	// Resources are the resources its configuration declares, as
	// "<type>.<name>".
	Resources map[string]bool
	// State is its local state, if it has one: the resources it has, and
	// where. Nil if the root has none, or keeps its state in a backend,
	// which isn't read here (see shared.previousAddressesOf for what
	// --managed-state reads).
	State managed.Resources
}

// observeRoot reads what the root in dir says of its resources.
func observeRoot(dir string) (observedRoot, error) {
	declared, err := engine.ConfiguredResources(dir)
	if err != nil {
		return observedRoot{}, err
	}
	obs := observedRoot{Resources: make(map[string]bool, len(declared))}
	for _, a := range declared {
		obs.Resources[a] = true
	}
	// The state a root with a backend has locally, if any, is a stale
	// copy or a stub.
	if _, err := os.Stat(filepath.Join(dir, BackendFileName)); err == nil {
		return obs, nil
	}
	state, err := managed.Load(context.Background(), []string{filepath.Join(dir, localStateFile)}, managed.Stores{})
	if err != nil || len(state) == 0 {
		return obs, nil //nolint:nilerr // no readable local state means the configuration alone says
	}
	obs.State = state
	return obs, nil
}

// verifyAddresses returns inferred (see engine.InferredAddresses), where
// the root observed says what it has: a resource its local state has is at
// the address the state gives it, and a resource of a root without one, at
// an address the configuration declares, is where it was inferred. Those
// are no longer unverified. A resource a local state doesn't have, a
// module call (the resources in it are at addresses only a state tells),
// and a resource of an address the configuration doesn't declare stay
// as they were.
func verifyAddresses(inferred []engine.Managed, obs observedRoot) []engine.Managed {
	verified := make([]engine.Managed, len(inferred))
	for i, m := range inferred {
		verified[i] = m
		if obs.State != nil {
			if found, ok := obs.State.Find(m.Type, m.ID); ok && found.Address != "" && tftypes.Same(found.Type, m.Type) {
				verified[i].Address, verified[i].Unverified = found.Address, false
			}
			continue
		}
		if m.Unverified && engine.AddressType(m.Address) != "" && obs.Resources[m.Address] {
			verified[i].Unverified = false
		}
	}
	return verified
}

// allVerified reports whether none of managed is unverified.
func allVerified(managed []engine.Managed) bool {
	for _, m := range managed {
		if m.Unverified {
			return false
		}
	}
	return len(managed) > 0
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// tfvars returns the variables environment env's terraform.tfvars sets,
// by name (see envVariable.tfvars).
func (m *stackMerge) tfvars(env int) map[string]hclwrite.Tokens {
	values := map[string]hclwrite.Tokens{}
	for _, v := range m.variables {
		if value, ok := v.tfvars(env); ok {
			values[v.name] = value
		}
	}
	return values
}

// imports returns the import IDs of environment env's resources, by the
// address of their instance in the merged root, which var.import_ids
// gives import blocks.
func (m *stackMerge) imports(env int) map[string]string {
	ids := map[string]string{}
	for _, r := range m.resources {
		if member := r.members[env]; member != nil && member.id != "" {
			ids[string(hclwrite.TokensForTraversal(r.instance()).Bytes())] = member.id
		}
		if member := r.members[env]; member != nil && r.resourceType == moduleType {
			call := addressText(r.instance())
			for rest, id := range member.moduleIDs {
				ids[call+"."+rest] = id
			}
		}
	}
	return ids
}

// identities returns the identities of the resources environment env
// imports by identity, by the address of their instance in the merged
// root, which var.import_identities gives import blocks; imports has
// their identities' IDs.
func (m *stackMerge) identities(env int) map[string]map[string]string {
	identities := map[string]map[string]string{}
	for _, r := range m.resources {
		member := r.members[env]
		if member == nil {
			continue
		}
		if member.identity != nil {
			identities[string(hclwrite.TokensForTraversal(r.instance()).Bytes())] = member.identity
		}
		if r.resourceType == moduleType {
			call := addressText(r.instance())
			for rest, identity := range member.moduleIdentities {
				identities[call+"."+rest] = identity
			}
		}
	}
	return identities
}

// importsByIdentity reports whether any environment imports a resource
// by identity: whether the merged root has var.import_identities.
func (m *stackMerge) importsByIdentity() bool {
	for e := range m.envs {
		if len(m.identities(e)) > 0 {
			return true
		}
	}
	return false
}

// importBy is what the merged root's import blocks import a resource by:
// its import ID, from var.import_ids, or its identity, from
// var.import_identities, as the environments' roots imported it.
type importBy struct{ argument, variable string }

var (
	importByID       = importBy{"id", hclfile.ImportIDsVariableName}
	importByIdentity = importBy{"identity", hclfile.ImportIdentitiesVariableName}
)

// value is the argument of an import block that imports the instance
// whose address key, an expression, gives.
func (b importBy) value(key string) string {
	return fmt.Sprintf("%s = var.%s[%s]", b.argument, b.variable, key)
}

// importKinds returns what the environments import each instance of the
// merged root by, by its address. One import block can't import an
// instance by ID in one environment and by identity in another, so it
// fails where they differ.
func (m *stackMerge) importKinds() (map[string]importBy, error) {
	kinds := map[string]importBy{}
	for e := range m.envs {
		identities := m.identities(e)
		for address := range m.imports(e) {
			kind := importByID
			if identities[address] != nil {
				kind = importByIdentity
			}
			if had, ok := kinds[address]; ok && had != kind {
				return nil, fmt.Errorf("%s: some environments import it by ID and others by identity, which one import block can't do; import it the same way in each", address)
			}
			kinds[address] = kind
		}
	}
	return kinds, nil
}

// importKindOf returns what kinds says the import block of addresses, the
// instances of one resource block or module resource, imports them by:
// by ID unless they are imported by identity. It fails where some are
// imported by ID and others by identity.
func importKindOf(kinds map[string]importBy, owner string, addresses ...string) (importBy, error) {
	kind, seen := importByID, false
	for _, address := range addresses {
		k, ok := kinds[address]
		if !ok {
			continue
		}
		if seen && k != kind {
			return importBy{}, fmt.Errorf("%s: some of its instances are imported by ID and others by identity, which one import block can't do; import them the same way", owner)
		}
		kind, seen = k, true
	}
	return kind, nil
}

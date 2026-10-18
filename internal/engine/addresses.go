// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Managed is a resource a root imports: its type and import ID, and its
// address in the root, such as module.logs.aws_s3_bucket.this[0].
type Managed struct {
	Type, ID string
	Address  string
	// Unverified, if set, says Address was inferred from what an older
	// checkpoint recorded rather than read from the root's import blocks:
	// for a resource moved into a module call, it is then the call alone,
	// such as module.logs.
	Unverified bool `json:",omitempty"`
}

// ImportedAddresses returns what the import blocks of dir import, and
// where, sorted by type and import ID; none if dir has no imports.tf.
func ImportedAddresses(dir string) ([]Managed, error) {
	targets, err := importTargets(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return managedOf(targets), nil
}

// managedOf returns targets (see importTargetsIn) as resources, sorted by
// type and import ID.
func managedOf(targets map[string]string) []Managed {
	managed := make([]Managed, 0, len(targets))
	for key, address := range targets {
		typ, id, _ := strings.Cut(key, "\x00")
		managed = append(managed, Managed{Type: typ, ID: id, Address: address})
	}
	sortManaged(managed)
	return managed
}

func sortManaged(managed []Managed) {
	sort.Slice(managed, func(i, j int) bool {
		if managed[i].Type != managed[j].Type {
			return managed[i].Type < managed[j].Type
		}
		return managed[i].ID < managed[j].ID
	})
}

// InferredAddresses returns the addresses of what result imports, for a
// result without Addresses, such as in a checkpoint an older release
// wrote: each resource at the address Generate labelled it with or, if it
// was moved into a module call, at the call, all unverified (see
// Managed.Unverified).
func InferredAddresses(result *Result) []Managed {
	if result == nil {
		return nil
	}
	calls := map[string]string{}
	for _, m := range result.Modules {
		if m.Declined != "" {
			continue
		}
		for _, from := range m.Resources {
			calls[from] = "module." + m.Name
		}
	}
	managed := make([]Managed, 0, len(result.Imported))
	for _, imp := range result.Imported {
		m := Managed{Type: imp.Type, ID: imp.ID, Address: imp.Type + "." + imp.Name, Unverified: true}
		if call, ok := calls[m.Address]; ok {
			m.Address = call
		}
		managed = append(managed, m)
	}
	sortManaged(managed)
	return managed
}

// Readdressed returns what now, the resources a root imports once
// generated again, has at another address than previous had it, as
// "<from> is now <to>", sorted. A resource of previous now doesn't have,
// such as one deleted in the cloud since, isn't readdressed; nor is one
// previous has only the module call of (see Managed.Unverified) that
// now has in that call.
func Readdressed(previous, now []Managed) []string {
	at := map[External]string{}
	for _, m := range now {
		at[External{Type: m.Type, ID: m.ID}] = m.Address
	}
	var changed []string
	for _, p := range previous {
		to, ok := at[External{Type: p.Type, ID: p.ID}]
		if !ok || to == p.Address {
			continue
		}
		if p.Unverified && strings.Count(p.Address, ".") == 1 && strings.HasPrefix(p.Address, "module.") && strings.HasPrefix(to, p.Address+".") {
			continue
		}
		changed = append(changed, fmt.Sprintf("%s is now %s", p.Address, to))
	}
	sort.Strings(changed)
	return changed
}

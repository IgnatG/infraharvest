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
// generated again, has at another address than previous had it, but for
// those moved moves there, as "<from> is now <to>", sorted. A resource of
// previous now doesn't have, such as one deleted in the cloud since,
// isn't readdressed; nor is one previous has only the module call of
// (see Managed.Unverified) that now has in that call.
func Readdressed(previous, now []Managed, moved []Move) []string {
	at := map[External]string{}
	for _, m := range now {
		at[External{Type: m.Type, ID: m.ID}] = m.Address
	}
	follows := map[Move]bool{}
	for _, m := range moved {
		follows[m] = true
	}
	var changed []string
	for _, p := range previous {
		to, ok := at[External{Type: p.Type, ID: p.ID}]
		if !ok || to == p.Address || follows[Move{From: p.Address, To: to}] {
			continue
		}
		if p.Unverified && isModuleCall(p.Address) && strings.HasPrefix(to, p.Address+".") {
			continue
		}
		changed = append(changed, fmt.Sprintf("%s is now %s", p.Address, to))
	}
	sort.Strings(changed)
	return changed
}

// Retyped returns the resources of previous that now, the resources a
// root imports once generated again, has as another type with the same
// import ID, such as an aws_alb that is an aws_lb now, as "<from> is now
// <to>, of another type", sorted. No moved block can follow that: a plan
// would replace the resource. A resource of both types in both, such as
// a bucket and its versioning, which share an ID, isn't retyped.
func Retyped(previous, now []Managed) []string {
	had := map[External]bool{}
	for _, p := range previous {
		had[External{Type: p.Type, ID: p.ID}] = true
	}
	byID := map[string][]Managed{}
	stays := map[External]bool{}
	for _, m := range now {
		key := External{Type: m.Type, ID: m.ID}
		stays[key] = true
		if !had[key] {
			byID[m.ID] = append(byID[m.ID], m)
		}
	}
	var changed []string
	for _, p := range previous {
		if stays[External{Type: p.Type, ID: p.ID}] {
			continue
		}
		for _, m := range byID[p.ID] {
			changed = append(changed, fmt.Sprintf("%s is now %s, of another type", p.Address, m.Address))
		}
	}
	sort.Strings(changed)
	return changed
}

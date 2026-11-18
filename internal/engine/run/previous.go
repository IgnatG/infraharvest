// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// Previous is what an earlier import generated in a root Generate
// generates again (see Options.Previous).
type Previous struct {
	// Addresses are where that import put the root's resources.
	Addresses []Managed
	// Labels are the labels it gave them, by type and import ID, such as
	// those of resources it then moved into module calls. A resource
	// without one has the label of its address, in the root or in a module
	// call.
	Labels map[External]string
	// Moved are the moved blocks of the root's moves.tf, which a plan may
	// not have applied yet: Generate keeps them, and its own moves follow
	// on from them.
	Moved []rootfile.Move
	// Removed are the removed blocks the root gets: those of its moves.tf,
	// which a plan may not have applied yet either, and those of the
	// resources it now hands over to other roots.
	Removed []rootfile.Removal
}

// LabelsByKey returns the label p says each resource had, by type and import
// ID (see Previous.Labels).
func (p *Previous) LabelsByKey() map[External]string {
	if p == nil {
		return nil
	}
	labels := map[External]string{}
	for _, m := range p.Addresses {
		// A resource in a module call has its label there too, which
		// stays if it still fits the resource (see keepsLabel): a run that
		// stopped before it had recorded its labels (see Labels) knows no
		// other.
		if label, ok := callLabel(m.Address); ok {
			labels[ResourceKey(m.Type, m.ID)] = label
		}
		if label, ok := rootLabel(m.Address); ok {
			labels[ResourceKey(m.Type, m.ID)] = label
		}
	}
	for key, label := range p.Labels {
		labels[key] = label
	}
	return labels
}

// Reserved returns the resource addresses of the root, as type.label,
// that a resource Generate labels may take only if it is the one given:
// those p's resources had, each for itself, and those p's moved and
// removed blocks name, for none.
func (p *Previous) Reserved() map[string]External {
	if p == nil {
		return nil
	}
	reserved := map[string]External{}
	var named []string
	for _, m := range p.Moved {
		named = append(named, m.From, m.To)
	}
	for _, r := range p.Removed {
		named = append(named, r.From)
	}
	for _, address := range named {
		if typ, label, ok := SplitRootResource(address); ok {
			reserved[typ+"."+label] = External{}
		}
	}
	// A resource a removed block hands over that comes back keeps its
	// address, and the block goes (see removals).
	for _, r := range p.Removed {
		if typ, label, ok := SplitRootResource(r.From); ok && len(r.Resources) == 1 && r.Resources[0].Address == r.From {
			reserved[typ+"."+label] = External{Type: r.Resources[0].Type, ID: r.Resources[0].ID}
		}
	}
	for _, m := range p.Addresses {
		if label, ok := rootLabel(m.Address); ok {
			reserved[m.Type+"."+label] = ResourceKey(m.Type, m.ID)
		}
	}
	return reserved
}

// SplitRootResource returns the type and label of address, a resource of the
// root such as aws_s3_bucket.logs or aws_s3_bucket.logs[0]; false for one
// in a module call.
func SplitRootResource(address string) (string, string, bool) {
	if strings.HasPrefix(address, "module.") {
		return "", "", false
	}
	typ, label, ok := strings.Cut(address, ".")
	if i := strings.IndexByte(label, '['); i >= 0 {
		label = label[:i]
	}
	return typ, label, ok && label != ""
}

// callLabel returns the label of address, if it is a resource in a module
// call, such as this for module.logs.aws_s3_bucket.this[0].
func callLabel(address string) (string, bool) {
	if !strings.HasPrefix(address, "module.") {
		return "", false
	}
	names := hclfile.AddressNames(address)
	for len(names) >= 2 && names[0] == "module" {
		names = names[2:]
	}
	if len(names) != 2 || names[1] == "" {
		return "", false
	}
	return names[1], true
}

// rootLabel returns the label of address, if it is a resource of the root.
func rootLabel(address string) (string, bool) {
	_, label, ok := SplitRootResource(address)
	return label, ok
}

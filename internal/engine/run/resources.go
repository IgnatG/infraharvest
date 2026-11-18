// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// RootResource is a resource another root of the same import manages.
type RootResource struct {
	Type, ID string
	// Name is the resource's name in that root, and Root that root's
	// directory, relative to the output directory.
	Name, Root string
}

// ARN is where the ARNs of a resource type whose IDs aren't ARNs name its
// resources: the ARN's service, and the resource type its resource part
// starts with, "" if the part is the ID alone.
type ARN struct {
	Service, ResourceType string
}

// External is a resource the import listed but doesn't manage, such as one
// the selection left out: the configuration may still refer to it. Name is
// the name it was listed by, from which the label of the data source that
// reads it is made; with none, the label is made from the ID.
type External struct {
	Type, ID string
	Name     string `json:",omitempty"`
}

// DataSource reads one resource of a type: the data source's type, and the
// argument that takes the resource's ID.
type DataSource struct {
	Type, Argument string
	// Provider is the local name of the provider that reads it, when it
	// isn't the one Type implies: google-beta for a root of google-beta,
	// which requires no google. "" otherwise.
	Provider string `json:",omitempty"`
}

// Names are names a root already uses, which Generate gives nothing it
// adds: resources as type.name, and data.type.name, module.name, var.name
// and local.name.
type Names map[string]bool

// HasType reports whether n has a resource of resourceType.
func (n Names) HasType(resourceType string) bool {
	for name := range n {
		if typ, _, ok := strings.Cut(name, "."); ok && typ == resourceType {
			return true
		}
	}
	return false
}

// ResourceKey is what a resource of type typ, imported by id, is told
// apart by from one run to the next, in a root, between roots and in the
// checkpoints: its type and its import ID, without the part the
// catalogue says changes while the resource stays the same (see
// catalogue.UnstableIDSuffix), such as the ?api-version= an azapi
// resource's ID ends with. That version is the newest stable one of the
// resource's type when it was imported, or none (see retryFallbacks),
// and changes as Azure releases new ones: the resource stays the same, so
// it keeps its root and its label, isn't added again by --incremental,
// and needs no moved block.
func ResourceKey(typ, id string) External {
	return External{Type: typ, ID: IdentityID(typ, id)}
}

// IdentityID is id without the part that changes while the resource
// stays the same, for a type the catalogue says has one; any other id as
// it is.
func IdentityID(typ, id string) string {
	suffix := catalogue.ForType(typ).UnstableIDSuffix(typ)
	if suffix == "" {
		return id
	}
	if base, _, ok := strings.Cut(id, suffix); ok {
		return base
	}
	return id
}

// ScopeLocals is what a root reads of its own account (or subscription,
// or project) and of its own region, as a root environments share reads
// them (see envmerge.MergeProfile): the literals that hold them become
// references to locals of data sources (see liftScope). A part without
// an ID stays literal, such as the region of a root of global services,
// whose provider is in a region that isn't the root's.
type ScopeLocals struct {
	Account, Region ScopeValue
}

// ScopeValue is one ID a root's literals hold: ID is the value, such as
// 123456789012 or eu-west-2, "" for none; Data the data source that reads
// it, such as aws_caller_identity.current; Local the local that holds it,
// such as aws_account_id; and Expr the expression that reads it, such as
// data.aws_caller_identity.current.account_id.
type ScopeValue struct {
	ID, Data, Local, Expr string
	// Provider is the local name of the provider that reads Data, when it
	// isn't the one its type implies: google-beta for the
	// google_client_config of a root of google-beta, which requires no
	// google. "" otherwise.
	Provider string `json:",omitempty"`
}

// Set reports whether v has an ID and says how to read it.
func (v ScopeValue) Set() bool {
	return v.ID != "" && v.Data != "" && v.Local != "" && v.Expr != ""
}

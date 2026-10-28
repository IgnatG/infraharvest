// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"context"
	"maps"
	"sync"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// Listed is a resource a provider call listed, with the tags read for it
// (see harvest.ProviderWithTags), which may be more than its lister
// recorded.
type Listed struct {
	harvest.Resource
	Tags map[string]string
}

// Sources are what the default selection of one provider call's listing
// is made of: which resources an import leaves out unless a selection
// file includes them, and why. Several sources say so; Sources decides
// between them (see Reason), so that a resource is left out for the same
// reason whichever way it is asked about. Each source keys resources by type and
// lister ID, "<type> <id>", as the provider capabilities do.
type Sources struct {
	// includeOwned keeps what the tags say another Terraform state or tool
	// manages, as --include-owned does.
	includeOwned bool
	// skipped are the reasons of the catalogue's skip rules, which an ID or
	// the attributes alone decide.
	skipped map[string]string
	// provider are the reasons of the provider's defaults, which take a
	// cloud API call to find (see harvest.ProviderWithSelectionDefaults).
	provider map[string]string
	// owners are what the tags say manages a resource (see engine.OwnedBy).
	owners map[string]engine.Owner
	// managed are where the states of --managed-state have a resource.
	managed map[string]string
	// inside and outside are the import scope's (see harvest.ImportScope).
	inside  map[string]bool
	outside map[string]string
}

// NewSources returns the sources of the default selection of listed: the catalogue's
// skip rules, what the tags say manages a resource unless includeOwned is
// set, and what provider finds, if the provider has defaults of its own.
// The provider sees each resource's tags among its attributes as
// tags.<key>, the way listers record the tags they read themselves, so
// that a tag can tell what owns a resource without another call. The
// error is the provider's; what it found before it failed is kept.
func NewSources(ctx context.Context, cat *catalogue.Catalogue, provider harvest.ProviderWithSelectionDefaults, listed []Listed, includeOwned bool) (*Sources, error) {
	s := &Sources{
		includeOwned: includeOwned,
		skipped:      map[string]string{},
		owners:       map[string]engine.Owner{},
		managed:      map[string]string{},
	}
	all := make([]harvest.Resource, 0, len(listed))
	for _, l := range listed {
		k := listedKey(l.Resource)
		if reason := cat.Skipped(l.Address.Type, l.Observed.ID, l.Observed.Attributes); reason != "" {
			s.skipped[k] = reason
		}
		if owner, ok := engine.OwnedBy(l.Tags); ok {
			s.owners[k] = owner
		}
		all = append(all, withTagAttributes(l.Resource, l.Tags))
	}
	if provider == nil {
		return s, nil
	}
	found, err := provider.ExcludedByDefault(ctx, all)
	s.provider = found
	return s, err
}

// withTagAttributes returns r with tags among its attributes, as
// tags.<key>, in a copy: what the lister recorded stays as it is, and a
// tag the lister recorded itself wins over the listed one. Without tags,
// r is returned as it is.
func withTagAttributes(r harvest.Resource, tags map[string]string) harvest.Resource {
	if len(tags) == 0 {
		return r
	}
	attributes := make(map[string]string, len(r.Observed.Attributes)+len(tags))
	for key, value := range tags {
		attributes["tags."+key] = value
	}
	maps.Copy(attributes, r.Observed.Attributes)
	observed := *r.Observed
	observed.Attributes = attributes
	r.Observed = &observed
	return r
}

// listedKey is how the provider capabilities key a listed resource: its
// type, one space, and its lister ID.
func listedKey(r harvest.Resource) string {
	return r.Address.Type + " " + r.Observed.ID
}

// Managed records that the Terraform state at where, one of
// --managed-state, has r, so that an import leaves it out.
func (s *Sources) Managed(r harvest.Resource, where string) {
	s.managed[listedKey(r)] = where
}

// Scope applies the import scope of the provider (see
// harvest.ImportScope): a resource it names is no longer left out because
// CloudFormation manages it, by the stacks that list it or by its tags,
// as the scope is those stacks; one outside it is left out whatever a
// selection file says.
func (s *Sources) Scope(scope *harvest.ImportScope) {
	s.inside, s.outside = scope.Inside, scope.Outside
}

// Reason returns why the default selection leaves out r, or "" if it
// doesn't. The import scope leaves out what is outside it; otherwise a
// Terraform state that has r says so first, as what it manages would
// otherwise have two owners, then the catalogue's skip rules, then the
// provider's defaults, then what the tags say manages r. A reason that
// CloudFormation manages r doesn't count for a resource inside the scope.
func (s *Sources) Reason(r harvest.Resource) string {
	k := listedKey(r)
	if reason := s.outside[k]; reason != "" {
		return reason
	}
	var reason string
	where, inState := s.managed[k]
	owner, tagged := s.owners[k]
	switch {
	case inState:
		reason = managedReason(where)
	case s.skipped[k] != "":
		reason = s.skipped[k]
	case s.provider[k] != "":
		reason = s.provider[k]
	case tagged && !s.includeOwned:
		reason = OwnedReason(owner)
	}
	if s.inside[k] && report.OwnerOf(reason) == report.OwnerCloudFormation {
		return ""
	}
	return reason
}

// Decide returns whether an import with the selection file f, nil for
// --all, takes l, which f names by its import ID id in scope. What the
// import scope leaves out stays out. An entry of f decides l; without
// one, f's rules do, then the defaults (see Reason), then f's defaults,
// except that a rule doesn't include what another Terraform state or tool
// manages (see File.byRuleFor). Without f, the defaults decide.
func (s *Sources) Decide(f *File, scope, id string, l Listed) Decision {
	if reason := s.outside[listedKey(l.Resource)]; reason != "" {
		return Decision{Reason: reason}
	}
	typ := l.Address.Type
	reason := s.Reason(l.Resource)
	unread := harvest.ListedByIdentity(l.Resource)
	switch {
	case f != nil && f.hasIn(scope, typ, id):
		return f.decideIn(scope, typ, id, l.RawName, l.Tags, unread)
	case f != nil:
		if ruled, ok := f.byRuleFor(typ, id, l.RawName, l.Tags, unread, reason); ok {
			return ruled
		}
		if reason != "" {
			return Decision{Reason: reason}
		}
		return f.decideIn(scope, typ, id, l.RawName, l.Tags, unread)
	case reason != "":
		return Decision{Reason: reason}
	}
	return Decision{Include: true}
}

// includeOwnedHint ends the reason a resource another tool owns is left
// out with: how to import it all the same.
const includeOwnedHint = "; --include-owned imports it"

// OwnedReason is why a resource whose tags say another tool manages it is
// left out. A Terraform or OpenTofu owner counts as Terraform's in the
// report (see report.TerraformPrefix), other tools as another tool's (see
// report.OtherToolPrefix).
func OwnedReason(o engine.Owner) string {
	switch {
	case o.TerraformState():
		return report.TerraformPrefix + " (" + o.Tool + " elsewhere, per its tag " + o.Tag + includeOwnedHint + ")"
	case o.Stack != "":
		return report.OtherToolPrefix + "CloudFormation stack " + o.Stack + " (per its tag " + o.Tag + includeOwnedHint + ")"
	default:
		return report.OtherToolPrefix + o.Tool + " (per its tag " + o.Tag + includeOwnedHint + ")"
	}
}

// OwnedBy returns what manages a resource whose tags say o manages it, one
// of the report.Owner constants, as OwnedReason gives its reason.
func OwnedBy(o engine.Owner) string {
	switch {
	case o.TerraformState():
		return report.OwnerTerraformTag
	case o.Stack != "":
		return report.OwnerCloudFormation
	default:
		return report.OwnerOtherTool
	}
}

// managedReason is why a resource the Terraform state at where has is
// left out.
func managedReason(where string) string {
	return report.TerraformPrefix + " (" + where + ")"
}

// Unwanted returns a function that reports whether an import with the
// selection file f, nil for --all, leaves out every resource of a type
// whatever is listed: a type the catalogue's default selection leaves out
// whole (see catalogue.Catalogue.SkippedWhole), unless f may include it
// (see File.mayInclude), and a type f itself leaves out whole (see
// File.excludesWhole). A type f excludes only some resources of isn't
// unwanted. The function is safe for concurrent use: the listers of an
// import's services ask it at once.
func Unwanted(cat *catalogue.Catalogue, f *File) func(typ string) bool {
	skipped := map[string]bool{}
	for _, typ := range cat.SkippedWhole() {
		skipped[typ] = true
	}
	if f == nil {
		return func(typ string) bool { return skipped[typ] }
	}
	// Listers ask once per resource they might look into further, and
	// the file's entries can be many: each type is worked out once.
	var decided sync.Map
	return func(typ string) bool {
		if unwanted, ok := decided.Load(typ); ok {
			return unwanted.(bool)
		}
		unwanted := !f.mayInclude(typ) && (skipped[typ] || f.excludesWhole(typ))
		decided.Store(typ, unwanted)
		return unwanted
	}
}

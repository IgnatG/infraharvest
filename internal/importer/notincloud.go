// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// listedHere is what one Import call's listing found in its scope, for an
// incremental import to tell which of the resources a root has are no
// longer in the cloud (see notInCloud). It takes no call of its own: it
// is what the listing returned.
type listedHere struct {
	// ids are the IDs of what the listing found, of any type, both import
	// IDs and the listers' own, in lower case: a resource counts as found
	// by either, whatever the case, as with an Azure ID that one API
	// spells otherwise, and whatever type lists it now.
	ids map[string]bool
	// types are the types the listing finds every resource of in its
	// scope (see typesListedInFull).
	types map[string]bool
}

// listedInScope returns what the listing of the call c found (see
// listedHere), from the resources of listed and those of more, such as
// the children of what the selection keeps. It returns nil, with why, when
// the listing can't tell what is gone: the provider can't name the account
// and region it lists, or pattern, the call's --layout, doesn't give each
// of them roots of their own, so a root may hold what another call lists;
// the resources come from what discover saved, which may be older than the
// roots; filters, or Resource Explorer's index, left some out; or the
// listing failed in part. As when discover drops the entries of its
// selection file (see Run.covers), what wasn't listed in full counts as
// unknown, not as gone. Failures to read tags don't count: they leave out
// no resource.
func listedInScope(provider harvest.Provider, c *callContext, pattern string, listed listing, failures []error, more map[string][]harvest.Resource) (*listedHere, string) {
	options := c.options
	if pattern == "" {
		pattern = DefaultLayout
	}
	switch {
	case c.scope == "":
		return nil, "the provider can't say which account and region it lists"
	case !strings.Contains(pattern, "{account}") || !strings.Contains(pattern, "{region}"):
		return nil, "--layout doesn't give each account and region roots of their own"
	case listed.saved:
		return nil, "the resources come from what discover saved (--reuse-inventory), which may be older than the roots"
	case len(options.filterArgs()) > 0:
		return nil, "the listing was filtered with --id, --tag or --where"
	case options.ResourceExplorer:
		return nil, "--resource-explorer leaves out the services its index shows nothing of"
	}
	for _, f := range failures {
		if !strings.HasPrefix(f.Error(), tagsFailure) {
			return nil, "the listing failed in part"
		}
	}
	here := &listedHere{ids: map[string]bool{}, types: typesListedInFull(provider, c)}
	for _, resources := range []map[string][]harvest.Resource{listed.resources, more} {
		for _, list := range resources {
			for _, r := range list {
				if r.Address == nil || r.Observed == nil {
					continue
				}
				here.ids[strings.ToLower(r.Observed.ID)] = true
				if id, ok := c.importID(r); ok {
					here.ids[strings.ToLower(id)] = true
				}
			}
		}
	}
	return here, ""
}

// typesListedInFull returns the types whose every resource in its scope
// a listing of the services of the call c finds: those the catalogue says
// one of the services lists, and those of its inventory entries, which a service
// that lists through an inventory API, such as Google's cloud_asset, maps
// resources to. Left out are the types such a service has no inventory
// entry for, which it maps to a type one resource at a time, as Azure's
// resource_graph does through Resource Manager, where a lookup that fails
// leaves the resource out unseen; the children of other types, which only
// the resources the selection keeps get (see withChildImports); the
// types the import doesn't list (see selection.Unwanted) or can't import; and
// the types the provider says it lists in part (see
// harvest.ProviderWithPartialTypes).
func typesListedInFull(provider harvest.Provider, c *callContext) map[string]bool {
	cat := c.cat
	types := map[string]bool{}
	if cat == nil {
		return types
	}
	children := map[string]bool{}
	for _, typ := range cat.Types() {
		if e, ok := cat.Entry(typ); ok {
			for _, child := range e.Children {
				children[child] = true
			}
		}
	}
	var partial []string
	if p, ok := provider.(harvest.ProviderWithPartialTypes); ok {
		partial = p.PartialTypes()
	}
	inventory := slices.ContainsFunc(c.services, func(s string) bool { return slices.Contains(inventoryServices, s) })
	for _, typ := range cat.Types() {
		e, ok := cat.Entry(typ)
		if !ok || children[typ] || e.NotImportable != "" || slices.Contains(c.unwanted, typ) || slices.Contains(partial, typ) {
			continue
		}
		switch {
		case e.Inventory != nil && inventory:
			// The inventory service maps the resources it lists to it.
		case e.Service == "" || !slices.Contains(c.services, e.Service):
			continue
		case slices.Contains(inventoryServices, e.Service):
			// Its service maps it one resource at a time.
			continue
		}
		types[typ] = true
	}
	return types
}

// notInCloud returns, sorted by address, the resources of existing, what
// a root has (see engine.Existing), that the listing didn't find although
// it lists every resource of their type in its scope: those the root has
// a resource block for, which its next plan would create again. Those
// only in a module call, or left out of the root, aren't. A nil h finds
// none.
func (h *listedHere) notInCloud(existing map[engine.External]string) []engine.Import {
	if h == nil {
		return nil
	}
	var gone []engine.Import
	for e, address := range existing {
		name, ok := strings.CutPrefix(address, e.Type+".")
		if !ok || name == "" || !h.types[e.Type] || h.ids[strings.ToLower(e.ID)] {
			continue
		}
		gone = append(gone, engine.Import{Type: e.Type, Name: name, ID: e.ID})
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].Type != gone[j].Type {
			return gone[i].Type < gone[j].Type
		}
		return gone[i].Name < gone[j].Name
	})
	return gone
}

// recordNotInCloud records gone, the resources of the root in dir that
// are no longer in the cloud (see listedHere.notInCloud), in the report,
// and logs how many there are.
func (r *Run) recordNotInCloud(dir string, gone []engine.Import) {
	if len(gone) == 0 {
		return
	}
	r.report.AddNotInCloud(relativePath(r.options.Out, dir), r.scope, gone)
	log.Printf("%s: no longer in the cloud: %d of the root's resources; delete their blocks from the root, or plan to create them again (see %s) (--incremental)", dir, len(gone), filepath.Join(r.options.Out, report.Dir, "report.md"))
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

// prune applies the lister's ID filters to its resources and drops
// duplicates, keeping the order. A resource is a duplicate of an earlier
// kept one with the same type and ID. Resources with the same type and
// name are not: two APIs can each have a stage named $default.
//
// Attribute filters are not applied here: they are for the listers that can
// pass them to the cloud API, and a resource whose lister recorded no such
// attribute must not be dropped for it.
//
// A lister without filters is left exactly as it is, duplicates included.
// When nothing is kept, Resources becomes nil.
func prune(s *ListerBase) {
	if len(s.filters) == 0 {
		return
	}

	// resourceKey identifies a resource for spotting duplicates. The ID
	// alone is not enough, since resources of different types may share
	// one.
	type resourceKey struct {
		resourceType, id string
	}
	kept := make(map[resourceKey]bool)
	var resources []Resource
	for _, resource := range s.Resources {
		if !passesIDFilters(s.filters, resource) {
			continue
		}
		key := resourceKey{resource.Address.Type, resource.Observed.ID}
		if kept[key] {
			continue
		}
		kept[key] = true
		resources = append(resources, resource)
	}
	s.Resources = resources
}

// passesIDFilters reports whether resource passes every ID filter among
// filters. Several ID filters on one service must all pass.
func passesIDFilters(filters []Filter, resource Resource) bool {
	for i := range filters {
		filter := &filters[i]
		if filter.Path == idPath && !filter.Keeps(resource) {
			return false
		}
	}
	return true
}

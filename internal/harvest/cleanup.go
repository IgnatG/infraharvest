// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

// Prune applies the ID filters among filters to resources and drops
// duplicates, keeping the order. A resource is a duplicate of an earlier
// kept one with the same type and ID. Resources with the same type and
// name are not: two APIs can each have a stage named $default.
//
// Attribute filters are not applied here: they are for the listers that can
// pass them to the cloud API, and a resource whose lister recorded no such
// attribute must not be dropped for it.
//
// Duplicates are dropped with or without filters: a lister that reaches a
// resource twice, such as through two parents or overlapping pages, would
// otherwise import it twice, under two names. A resource without an
// address or an observation is kept as it is. When nothing is kept, it
// returns nil.
func Prune(resources []Resource, filters []Filter) []Resource {
	// resourceKey identifies a resource for spotting duplicates. The ID
	// alone is not enough, since resources of different types may share
	// one.
	type resourceKey struct {
		resourceType, id string
	}
	kept := make(map[resourceKey]bool)
	var pruned []Resource
	for _, resource := range resources {
		if len(filters) > 0 && !passesIDFilters(filters, resource) {
			continue
		}
		if resource.Address == nil || resource.Observed == nil {
			pruned = append(pruned, resource)
			continue
		}
		key := resourceKey{resource.Address.Type, resource.Observed.ID}
		if kept[key] {
			continue
		}
		kept[key] = true
		pruned = append(pruned, resource)
	}
	return pruned
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

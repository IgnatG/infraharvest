// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// resourceName is what a resource's block label is made from (see
// engine.Label): the value of the provider's name tag (see
// harvest.Profile.NameTag), which people read it by, such as AWS's Name
// tag shop-private-eu-west-2a for a subnet the lister names by its ID;
// else the name the lister gave it.
func resourceName(nameTag string, r harvest.Resource, tags map[string]string) string {
	if name := strings.TrimSpace(tags[nameTag]); nameTag != "" && name != "" {
		return name
	}
	return r.RawName
}

// resourceNames returns what each resource's block label is made from:
// its resourceName, in which each word (as strings.Fields splits it) that
// is the ID of another resource listed or, as a child, in
// resourcesByService, becomes that resource's own name. So an
// association a lister names after the subnet it belongs to, by the
// subnet's ID, is labelled after the subnet's Name tag, and an Elastic IP
// named after its instance after the instance's. Where several
// resources have the ID, such as a bucket and its versioning, the name
// comes from the one of the first type in alphabetical order, among those
// whose name isn't their ID.
func resourceNames(nameTag string, listed listing, resourcesByService map[string][]harvest.Resource) func(harvest.Resource) string {
	nameOf := func(r harvest.Resource) string { return resourceName(nameTag, r, listed.tagsOf(r)) }
	type named struct{ resourceType, name string }
	byID := map[string]named{}
	add := func(resources map[string][]harvest.Resource) {
		for _, list := range resources {
			for _, r := range list {
				id, name := r.Observed.ID, nameOf(r)
				if id == "" || name == "" || name == id {
					continue
				}
				if seen, ok := byID[id]; !ok || r.Address.Type < seen.resourceType || (r.Address.Type == seen.resourceType && name < seen.name) {
					byID[id] = named{r.Address.Type, name}
				}
			}
		}
	}
	add(listed.resources)
	add(resourcesByService)
	return func(r harvest.Resource) string {
		name := nameOf(r)
		words := strings.Fields(name)
		resolved := false
		for i, word := range words {
			if other, ok := byID[word]; ok && word != r.Observed.ID {
				words[i] = other.name
				resolved = true
			}
		}
		if !resolved {
			return name
		}
		return strings.Join(words, " ")
	}
}

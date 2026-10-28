// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// camelWord is where a camelCase service name, such as schedulerJobs,
// starts a word.
var camelWord = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// configFile names the file of a root that a resource listed by service
// goes in (see rootfile.Import.File), as the provider's profile lays out
// its resources: network.tf for the network, else the service's, such as
// s3.tf or scheduler_jobs.tf. For a service whose name says nothing of a
// resource, one that lists through an inventory API (see
// harvest.Layout.InventoryServices) or the query service, the
// catalogue's service for the type counts, or else the type's family,
// such as storage.tf for every azurerm_storage_* type; for the query
// service, which lists the types no other service lists, the service
// that lists the most types of the family it is named after (see
// listedFamily); or else the type itself, such as backend_service.tf.
func configFile(profile harvest.Profile, service, typ string, cat *catalogue.Catalogue) string {
	layout := profile.Layout
	if query := service == harvest.QueryService; query || slices.Contains(layout.InventoryServices, service) {
		inventory := service
		service = ""
		if cat != nil {
			if e, ok := cat.Entry(typ); ok && e.Service != inventory {
				service = e.Service
			}
		}
		if service == "" {
			// google_compute_subnetwork: subnetwork.
			_, name, _ := strings.Cut(typ, "_")
			if layout.TypeNameTrim != "" {
				name = strings.TrimPrefix(name, layout.TypeNameTrim)
			}
			if slices.ContainsFunc(layout.NetworkTypes, func(n string) bool { return namedAfter(name, n) }) {
				return "network.tf"
			}
			family := ""
			for f := range layout.TypeFamilies {
				if namedAfter(name, f) && len(f) > len(family) {
					family = f
				}
			}
			if family != "" {
				return layout.TypeFamilies[family] + ".tf"
			}
			if query {
				service = listedFamily(profile, typ)
			}
			if service == "" {
				return name + ".tf"
			}
		}
	}
	if slices.Contains(layout.NetworkServices, service) {
		return "network.tf"
	}
	return strings.ToLower(camelWord.ReplaceAllString(service, "${1}_${2}")) + ".tf"
}

// listedFamily returns the service that lists the most of the types of
// the family typ is named after, by the longest such family that one of
// the provider's services lists types of (see harvest.Profile.ListedTypes),
// the first by name of those that list as many, and not counting the
// inventory services, whose names say nothing of a resource: s3 for
// aws_s3_bucket_inventory, as it lists aws_s3_bucket. It returns "" for a
// type of no listed family.
func listedFamily(profile harvest.Profile, typ string) string {
	prefix, name, _ := strings.Cut(typ, "_")
	words := strings.Split(name, "_")
	for n := len(words); n > 0; n-- {
		family := prefix + "_" + strings.Join(words[:n], "_")
		count := map[string]int{}
		for t, service := range profile.ListedTypes {
			if namedAfter(t, family) && !slices.Contains(profile.Layout.InventoryServices, service) {
				count[service]++
			}
		}
		best := ""
		for _, service := range slices.Sorted(maps.Keys(count)) {
			if count[service] > count[best] {
				best = service
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// namedAfter reports whether a type's name, without its prefix, is prefix
// or starts with it as a word, as key_vault_secret does with key_vault.
func namedAfter(name, prefix string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+"_")
}

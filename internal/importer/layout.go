// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"regexp"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// inventoryServices list every type through a cloud's inventory API, so
// their name says nothing about a resource.
var inventoryServices = []string{"cloud_asset", "resource_graph"}

// camelWord is where a camelCase service name, such as schedulerJobs,
// starts a word.
var camelWord = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// configFile names the file of a root that a resource listed by service
// goes in (see engine.Import.File): network.tf for the network, else the
// service's, such as s3.tf or scheduler_jobs.tf. For a service that
// lists through an inventory API, the catalogue's service for the type
// counts, or else the type's family (see harvest.Layout), such as
// storage.tf for every azurerm_storage_* type, or else the type itself,
// such as backend_service.tf.
func configFile(layout harvest.Layout, service, typ string, cat *catalogue.Catalogue) string {
	if slices.Contains(inventoryServices, service) {
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
			name = strings.TrimPrefix(name, "compute_")
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
			return name + ".tf"
		}
	}
	if slices.Contains(layout.NetworkServices, service) {
		return "network.tf"
	}
	return strings.ToLower(camelWord.ReplaceAllString(service, "${1}_${2}")) + ".tf"
}

// namedAfter reports whether a type's name, without its prefix, is prefix
// or starts with it as a word, as key_vault_secret does with key_vault.
func namedAfter(name, prefix string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+"_")
}

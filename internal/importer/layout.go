// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"regexp"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// networkServices are, by provider, the services whose resources make up
// the network, which share network.tf: a VPC is read with its subnets,
// route tables and gateways.
var networkServices = map[string][]string{
	"aws": {
		"customer_gateway", "eip", "eni", "igw", "nacl", "nat", "route_table", "sg", "subnet",
		"transit_gateway", "vpc", "vpc_endpoint", "vpc_peering", "vpn_connection", "vpn_gateway",
	},
	"azurerm": {
		"network_interface", "network_security_group", "public_ip", "subnet", "virtual_network",
	},
}

// networkTypes are, by provider, the network's resource types, without
// their prefix, for services that list through an inventory API: these
// types, and those whose name starts with one, such as
// azurerm_private_dns_a_record.
var networkTypes = map[string][]string{
	"google": {
		"address", "firewall", "global_address", "ha_vpn_gateway", "interconnect_attachment", "network",
		"route", "router", "subnetwork", "vpn_gateway", "vpn_tunnel",
	},
	"azurerm": {
		"network_interface", "network_security_group", "network_watcher", "private_dns", "private_endpoint",
		"public_ip", "route_table", "subnet", "virtual_network",
	},
}

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
// counts, or else the type itself, such as key_vault.tf.
func configFile(provider, service, typ string, cat *catalogue.Catalogue) string {
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
			for _, n := range networkTypes[provider] {
				if name == n || strings.HasPrefix(name, n+"_") {
					return "network.tf"
				}
			}
			return name + ".tf"
		}
	}
	if slices.Contains(networkServices[provider], service) {
		return "network.tf"
	}
	return strings.ToLower(camelWord.ReplaceAllString(service, "${1}_${2}")) + ".tf"
}

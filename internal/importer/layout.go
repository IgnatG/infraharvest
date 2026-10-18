// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"regexp"
	"slices"
	"strings"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
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
// azurerm_private_dns_a_record, or azurerm_route and
// azurerm_network_security_rule, which go with their route table and
// security group.
var networkTypes = map[string][]string{
	"google": {
		"address", "firewall", "global_address", "ha_vpn_gateway", "interconnect_attachment", "network",
		"route", "router", "subnetwork", "vpn_gateway", "vpn_tunnel",
	},
	"azurerm": {
		"application_security_group", "nat_gateway", "network", "private_dns", "private_endpoint",
		"public_ip", "route", "subnet", "virtual_network",
	},
}

// typeFamilies are, by provider, families of resource types, without
// their prefix, that share a file when a service lists them through an
// inventory API, by the file's name: the types named after a family, such
// as azurerm_storage_account and azurerm_storage_container after storage,
// go in its file, by the longest family they are named after. Where
// another service lists some of a family's types, the family's file is
// that service's, such as Google's gcs.tf for buckets and iam.tf for the
// project's IAM members, which service accounts and custom roles join.
var typeFamilies = map[string]map[string]string{
	"google": {
		"bigquery": "bigquery", "cloud_run": "cloud_run", "cloudfunctions": "cloudfunctions", "cloudfunctions2": "cloudfunctions",
		"container": "container", "dns": "dns", "kms": "kms", "logging": "logging", "monitoring": "monitoring",
		"project": "project", "project_iam": "iam", "pubsub": "pubsub", "secret_manager": "secret_manager",
		"service_account": "iam", "sql": "cloudsql", "storage": "gcs",
	},
	"azurerm": {
		"cosmosdb": "cosmosdb", "dns": "dns", "key_vault": "key_vault", "kubernetes_cluster": "kubernetes_cluster",
		"lb": "lb", "linux_virtual_machine": "virtual_machine", "log_analytics": "log_analytics", "monitor": "monitor",
		"mssql": "mssql", "mysql": "mysql", "postgresql": "postgresql", "storage": "storage",
		"virtual_machine": "virtual_machine", "windows_virtual_machine": "virtual_machine",
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
// counts, or else the type's family (see typeFamilies), such as
// storage.tf for every azurerm_storage_* type, or else the type itself,
// such as backend_service.tf.
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
			if slices.ContainsFunc(networkTypes[provider], func(n string) bool { return namedAfter(name, n) }) {
				return "network.tf"
			}
			family := ""
			for f := range typeFamilies[provider] {
				if namedAfter(name, f) && len(f) > len(family) {
					family = f
				}
			}
			if family != "" {
				return typeFamilies[provider][family] + ".tf"
			}
			return name + ".tf"
		}
	}
	if slices.Contains(networkServices[provider], service) {
		return "network.tf"
	}
	return strings.ToLower(camelWord.ReplaceAllString(service, "${1}_${2}")) + ".tf"
}

// namedAfter reports whether a type's name, without its prefix, is prefix
// or starts with it as a word, as key_vault_secret does with key_vault.
func namedAfter(name, prefix string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+"_")
}

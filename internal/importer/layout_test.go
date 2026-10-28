// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	awsprovider "github.com/IgnatG/infraharvest/internal/providers/aws"
	azureprovider "github.com/IgnatG/infraharvest/internal/providers/azure"
	googleprovider "github.com/IgnatG/infraharvest/internal/providers/google"
)

// TestConfigFile lays out the resources of each provider by its own
// profile.
func TestConfigFile(t *testing.T) {
	google := catalogue.MustBuiltin("google")
	profiles := map[string]harvest.Profile{}
	for _, p := range []harvest.Provider{&awsprovider.Provider{}, &azureprovider.Provider{}, &googleprovider.Provider{}} {
		profiles[p.Name()] = p.Profile()
	}
	for _, tc := range []struct {
		provider, service, typ string
		cat                    *catalogue.Catalogue
		want                   string
	}{
		{"aws", "subnet", "aws_subnet", nil, "network.tf"},
		{"aws", "sg", "aws_vpc_security_group_ingress_rule", nil, "network.tf"},
		{"aws", "s3", "aws_s3_bucket_versioning", nil, "s3.tf"},
		{"aws", "ec2_instance", "aws_instance", nil, "ec2_instance.tf"},
		{"google", "schedulerJobs", "google_cloud_scheduler_job", nil, "scheduler_jobs.tf"},
		{"google", "cloud_asset", "google_compute_backend_service", google, "backend_service.tf"},
		{"google", "cloud_asset", "google_compute_subnetwork", google, "network.tf"},
		{"google", "cloud_asset", "google_storage_bucket", google, "gcs.tf"},
		{"azurerm", "resource_graph", "azurerm_virtual_network", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_private_dns_zone", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_key_vault", nil, "key_vault.tf"},
		// A family of types shares a file, the network's types network.tf.
		{"google", "cloud_asset", "google_dns_managed_zone", google, "dns.tf"},
		{"google", "dns", "google_dns_record_set", google, "dns.tf"},
		{"google", "cloud_asset", "google_storage_bucket_iam_member", google, "gcs.tf"},
		{"google", "cloud_asset", "google_pubsub_topic", google, "pubsub.tf"},
		{"google", "cloud_asset", "google_service_account", google, "iam.tf"},
		{"google", "cloud_asset", "google_project_iam_custom_role", google, "iam.tf"},
		{"google", "iam", "google_project_iam_member", google, "iam.tf"},
		{"google", "cloud_asset", "google_project", google, "project.tf"},
		{"google", "cloud_asset", "google_sql_database_instance", google, "cloudsql.tf"},
		{"google", "cloudsql", "google_sql_database", google, "cloudsql.tf"},
		{"google", "cloud_asset", "google_cloudfunctions2_function", google, "cloudfunctions.tf"},
		{"azurerm", "resource_graph", "azurerm_key_vault_secret", nil, "key_vault.tf"},
		{"azurerm", "resource_graph", "azurerm_storage_account", nil, "storage.tf"},
		{"azurerm", "resource_graph", "azurerm_storage_container", nil, "storage.tf"},
		{"azurerm", "resource_graph", "azurerm_log_analytics_workspace", nil, "log_analytics.tf"},
		{"azurerm", "resource_graph", "azurerm_log_analytics_solution", nil, "log_analytics.tf"},
		{"azurerm", "resource_graph", "azurerm_route", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_route_table", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_network_security_rule", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_nat_gateway", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_subnet_route_table_association", nil, "network.tf"},
		{"azurerm", "resource_graph", "azurerm_linux_virtual_machine", nil, "virtual_machine.tf"},
		{"azurerm", "resource_graph", "azurerm_virtual_machine_extension", nil, "virtual_machine.tf"},
		{"azurerm", "resource_graph", "azurerm_managed_disk", nil, "managed_disk.tf"},
		{"azurerm", "managed_identity", "azurerm_user_assigned_identity", nil, "managed_identity.tf"},
		// What the query service lists goes with its family, as the
		// services that list the family's other types lay them out.
		{"aws", "query", "aws_s3_bucket_inventory", nil, "s3.tf"},
		{"aws", "query", "aws_cloudwatch_log_subscription_filter", nil, "logs.tf"},
		{"aws", "query", "aws_vpc_ipv4_cidr_block_association", nil, "network.tf"},
		{"aws", "query", "aws_flow_log", nil, "flow_log.tf"},
		{"google", "query", "google_pubsub_schema", google, "pubsub.tf"},
		{"google", "query", "google_compute_subnetwork", google, "network.tf"},
		{"azurerm", "query", "azurerm_storage_sync", nil, "storage.tf"},
	} {
		if got := configFile(profiles[tc.provider], tc.service, tc.typ, tc.cat); got != tc.want {
			t.Errorf("configFile(%s, %s, %s) = %s, want %s", tc.provider, tc.service, tc.typ, got, tc.want)
		}
	}
}

// The services that list through an inventory API, and what starts the
// names of their types without saying what they are, are the provider's
// own (see harvest.Layout): another provider's inventory service is a
// service like any other.
func TestConfigFileInventoryOfTheProfile(t *testing.T) {
	layout := harvest.Layout{InventoryServices: []string{"assets"}}
	for service, want := range map[string]string{"assets": "compute_disk.tf", "cloud_asset": "cloud_asset.tf"} {
		if got := configFile(harvest.Profile{Layout: layout}, service, "google_compute_disk", nil); got != want {
			t.Errorf("%s: %s, want %s", service, got, want)
		}
	}
	layout.TypeNameTrim = "compute_"
	if got := configFile(harvest.Profile{Layout: layout}, "assets", "google_compute_disk", nil); got != "disk.tf" {
		t.Errorf("with the trim: %s, want disk.tf", got)
	}
}

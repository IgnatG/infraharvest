// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
)

func TestConfigFile(t *testing.T) {
	google := catalogue.MustBuiltin("google")
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
	} {
		if got := configFile(tc.provider, tc.service, tc.typ, tc.cat); got != tc.want {
			t.Errorf("configFile(%s, %s, %s) = %s, want %s", tc.provider, tc.service, tc.typ, got, tc.want)
		}
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
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
		{"azurerm", "managed_identity", "azurerm_user_assigned_identity", nil, "managed_identity.tf"},
	} {
		if got := configFile(tc.provider, tc.service, tc.typ, tc.cat); got != tc.want {
			t.Errorf("configFile(%s, %s, %s) = %s, want %s", tc.provider, tc.service, tc.typ, got, tc.want)
		}
	}
}

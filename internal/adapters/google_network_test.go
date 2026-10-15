// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"strings"
	"testing"
)

const googleNetworkCluster = `resource "google_compute_network" "main" {
  auto_create_subnetworks                   = false
  bgp_best_path_selection_mode              = "LEGACY"
  delete_default_routes_on_create           = false
  deletion_policy                           = "DELETE"
  description                               = "app network"
  mtu                                       = 1460
  name                                      = "main"
  network_firewall_policy_enforcement_order = "AFTER_CLASSIC_FIREWALL"
  project                                   = "acme"
  routing_mode                              = "REGIONAL"
}
resource "google_compute_subnetwork" "app" {
  ip_cidr_range            = "10.10.0.0/24"
  name                     = "app"
  network                  = google_compute_network.main.self_link
  private_ip_google_access = true
  project                  = "acme"
  region                   = "us-central1"
  stack_type               = "IPV4_ONLY"
  log_config {
    aggregation_interval = "INTERVAL_10_MIN"
    flow_sampling        = 0.5
    metadata             = "INCLUDE_ALL_METADATA"
  }
  secondary_ip_range {
    ip_cidr_range = "10.20.0.0/16"
    range_name    = "pods"
  }
}
resource "google_compute_firewall" "https" {
  deletion_policy = "DELETE"
  description   = null
  direction     = "INGRESS"
  name          = "allow-https"
  network       = google_compute_network.main.self_link
  priority      = 1000
  project       = "acme"
  source_ranges = ["10.0.0.0/8"]
  target_tags   = ["web"]
  allow {
    ports    = ["443"]
    protocol = "tcp"
  }
}
resource "google_compute_firewall" "egress" {
  destination_ranges = ["0.0.0.0/0"]
  direction          = "EGRESS"
  name               = "deny-all-egress"
  network            = google_compute_network.main.self_link
  project            = "acme"
  source_ranges      = []
  deny {
    protocol = "all"
  }
}
`

func TestGoogleNetworkMapsEveryMember(t *testing.T) {
	c := cluster(t, googleNetworkCluster)
	call, err := GoogleNetwork.Map(c)
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, GoogleNetwork, call)
	for _, want := range []string{
		`project_id = "acme"`,
		`network_name = "main"`,
		"auto_create_subnetworks = false",
		`routing_mode = "REGIONAL"`,
		`description = "app network"`,
		"mtu = 1460",
		`network_firewall_policy_enforcement_order = "AFTER_CLASSIC_FIREWALL"`,
		`subnets = [{ subnet_name = "app" subnet_region = "us-central1" subnet_ip = "10.10.0.0/24" subnet_private_access = true stack_type = "IPV4_ONLY" subnet_flow_logs = "true" subnet_flow_logs_interval = "INTERVAL_10_MIN" subnet_flow_logs_sampling = 0.5 subnet_flow_logs_metadata = "INCLUDE_ALL_METADATA" }]`,
		`secondary_ranges = { "app" = [{ range_name = "pods" ip_cidr_range = "10.20.0.0/16" }] }`,
		`{ name = "allow-https" direction = "INGRESS" ranges = ["10.0.0.0/8"] priority = 1000 target_tags = ["web"] allow = [{ protocol = "tcp" ports = ["443"] }] }`,
		`{ name = "deny-all-egress" direction = "EGRESS" ranges = ["0.0.0.0/0"] deny = [{ protocol = "all" }] }`,
	} {
		if !strings.Contains(got, squash(want)) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	wantAddresses := map[string]string{
		"google_compute_network.main":    "module.vpc.google_compute_network.network",
		"google_compute_subnetwork.app":  `module.subnets.google_compute_subnetwork.subnetwork["us-central1/app"]`,
		"google_compute_firewall.https":  `module.firewall_rules.google_compute_firewall.rules["allow-https"]`,
		"google_compute_firewall.egress": `module.firewall_rules.google_compute_firewall.rules["deny-all-egress"]`,
	}
	if !reflect.DeepEqual(call.Addresses, wantAddresses) {
		t.Errorf("addresses: got %v, want %v", call.Addresses, wantAddresses)
	}
	if got := call.Outputs["google_compute_subnetwork.app"]["self_link"]; got != `subnets["us-central1/app"].self_link` {
		t.Errorf("subnetwork self link output: %q", got)
	}
	if got := call.Outputs["google_compute_network.main"]["self_link"]; got != "network_self_link" {
		t.Errorf("network self link output: %q", got)
	}
}

func TestGoogleNetworkDeclines(t *testing.T) {
	for name, tc := range map[string]struct{ from, to string }{
		"auto mode":           {"auto_create_subnetworks                   = false", "auto_create_subnetworks = true"},
		"auto mode, default":  {"auto_create_subnetworks                   = false", ""},
		"another project":     {`network                  = google_compute_network.main.self_link` + "\n  private_ip_google_access = true\n  project                  = \"acme\"", "network = google_compute_network.main.self_link\n  private_ip_google_access = true\n  project = \"other\""},
		"a name not literal":  {`name          = "allow-https"`, "name = local.rule"},
		"both ranges":         {`source_ranges = ["10.0.0.0/8"]`, "source_ranges = [\"10.0.0.0/8\"]\n  destination_ranges = [\"10.1.0.0/16\"]"},
		"an unknown argument": {`mtu                                       = 1460`, "mtu = 1460\n  params { resource_manager_tags = {} }"},
		"a deletion policy":   {`deletion_policy                           = "DELETE"`, `deletion_policy = "PREVENT"`},
	} {
		src := strings.Replace(googleNetworkCluster, tc.from, tc.to, 1)
		if src == googleNetworkCluster {
			t.Fatalf("%s: the replacement doesn't apply", name)
		}
		if _, err := GoogleNetwork.Map(cluster(t, src)); !IsDecline(err) {
			t.Errorf("%s: got %v, want a decline", name, err)
		}
	}
}

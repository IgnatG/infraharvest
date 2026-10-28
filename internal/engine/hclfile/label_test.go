// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import "testing"

func TestLabel(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"vpc-7fe7f292":               "vpc_7fe7f292",
		"/infraharvest-e2e/endpoint": "infraharvest_e2e_endpoint",
		"Orders Queue":               "orders_queue",
		"2024-backups":               "r_2024_backups",
		"--":                         "resource",
		// Words of camelCase names stay apart; before, these ran together
		// (allowhttpapi).
		"AllowHttpApi":   "allow_http_api",
		"HTTPApiGateway": "http_api_gateway",
		"Ipv4Address":    "ipv4_address",
		"Z1D633PJN98FT9": "z1d633pjn98ft9",
		// A parent's name before a child's that starts with it is said once.
		"shop_shop-web":                             "shop_web",
		"ih-sbx-terraform_ih-sbx-terraform-web":     "ih_sbx_terraform_web",
		"$default $default":                         "default",
		"web 10.0.0.10":                             "web_10_0_0_10",
		"web_10_0_0_0":                              "web_10_0_0_0",
		"api.ih-sbx.internal api.ih-sbx.internal A": "api_ih_sbx_internal_a",
	} {
		if got := Label(name); got != want {
			t.Errorf("Label(%q): got %q, want %q", name, got, want)
		}
	}
}

// A module call around the only resource of its type is named after the
// type: module "main" would say nothing, and a root with a VPC and a bucket
// module would call them main and main_2.
func TestCallName(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_vpc.main":       "vpc",
		"aws_s3_bucket.main": "s3_bucket",
		"aws_s3_bucket.logs": "logs",
		"aws_vpc.main_2":     "main_2",
	} {
		if got := CallName(address); got != want {
			t.Errorf("CallName(%q): got %q, want %q", address, got, want)
		}
	}
}

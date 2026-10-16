// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"slices"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// An argument is the default when it is a literal equal to the input's
// default, in its type; before, calls set every input the resources had,
// such as the network module's mtu = 0.
func TestIsDefault(t *testing.T) {
	for _, tt := range []struct {
		input string
		value hclwrite.Tokens
		want  bool
	}{
		{"mtu", hclwrite.TokensForValue(cty.Zero), true},
		{"mtu", hclwrite.TokensForValue(cty.NumberIntVal(1460)), false},
		{"bgp_best_path_selection_mode", hclwrite.TokensForValue(cty.StringVal("LEGACY")), true},
		{"bgp_best_path_selection_mode", hclwrite.TokensForValue(cty.StringVal("STANDARD")), false},
		{"enable_ipv6_ula", hclwrite.TokensForValue(cty.False), true},
		{"description", hclwrite.TokensForValue(cty.StringVal("")), true},
		{"description", hclwrite.TokensForTraversal(nil), false},
		{"mtu", hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: "mtu"}}), false},
		// routing_mode's default is the module's GLOBAL, not Google's REGIONAL.
		{"routing_mode", hclwrite.TokensForValue(cty.StringVal("REGIONAL")), false},
		{"network_name", hclwrite.TokensForValue(cty.StringVal("main")), false},
	} {
		if got := GoogleNetwork.IsDefault(tt.input, tt.value); got != tt.want {
			t.Errorf("IsDefault(%s, %s): got %v, want %v", tt.input, tt.value.Bytes(), got, tt.want)
		}
	}
}

// Every default is of an input the adapter sets.
func TestDefaultsAreInputs(t *testing.T) {
	for _, provider := range Providers() {
		for _, a := range For(provider) {
			for input := range a.Defaults {
				if !slices.Contains(a.Inputs, input) {
					t.Errorf("%s: default for %s, which isn't one of its inputs", a.Source, input)
				}
			}
		}
	}
}

// The AWS modules' defaults: a call leaves out what the module would set
// anyway, and keeps what differs, such as the VPC module's
// enable_dns_hostnames = false, whose default is true.
func TestAWSModuleDefaults(t *testing.T) {
	for _, tt := range []struct {
		adapter *Adapter
		input   string
		value   cty.Value
		want    bool
	}{
		{&VPC, "enable_dns_hostnames", cty.True, true},
		{&VPC, "enable_dns_hostnames", cty.False, false},
		{&VPC, "public_subnet_suffix", cty.StringVal("public"), true},
		{&VPC, "private_subnet_suffix", cty.StringVal("app"), false},
		{&S3Bucket, "block_public_acls", cty.True, true},
		{&S3Bucket, "block_public_acls", cty.False, false},
		{&S3Bucket, "object_ownership", cty.StringVal("BucketOwnerEnforced"), true},
		{&IAMRole, "path", cty.StringVal("/"), true},
		{&IAMRole, "max_session_duration", cty.NumberIntVal(3600), true},
		{&IAMRole, "max_session_duration", cty.NumberIntVal(7200), false},
	} {
		if got := tt.adapter.IsDefault(tt.input, hclwrite.TokensForValue(tt.value)); got != tt.want {
			t.Errorf("%s: IsDefault(%s, %#v): got %v, want %v", tt.adapter.Source, tt.input, tt.value, got, tt.want)
		}
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package typemap_test

import (
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/pkg/typemap"
)

func resolver(t *testing.T) *typemap.Resolver {
	t.Helper()
	r, err := typemap.New()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProviders(t *testing.T) {
	t.Parallel()
	got := resolver(t).Providers()
	for _, want := range []string{"aws", "azurerm", "google"} {
		if !slices.Contains(got, want) {
			t.Errorf("Providers() = %v, want %s among them", got, want)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("Providers() = %v, want it sorted", got)
	}
}

func TestFromInventoryType(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for _, tc := range []struct {
		cloud, assetType string
		want             []string
	}{
		{"google", "compute.googleapis.com/Network", []string{"google_compute_network"}},
		{"GOOGLE", "COMPUTE.googleapis.com/network", []string{"google_compute_network"}},
		{"gcp", "compute.googleapis.com/Network", []string{"google_compute_network"}},
		{"google", "compute.googleapis.com/Address", []string{"google_compute_address", "google_compute_global_address"}},
		{"aws", "AWS::S3::Bucket", []string{"aws_s3_bucket"}},
		{"aws", "compute.googleapis.com/Network", nil},
		{"google", "AWS::S3::Bucket", nil},
		{"nowhere", "AWS::S3::Bucket", nil},
		{"azure", "Microsoft.Network/virtualNetworks", nil},
	} {
		if got := r.FromInventoryType(tc.cloud, tc.assetType); !slices.Equal(got, tc.want) {
			t.Errorf("FromInventoryType(%q, %q) = %v, want %v", tc.cloud, tc.assetType, got, tc.want)
		}
	}
}

// A name tells apart the types an inventory type covers.
func TestFromInventoryName(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	if got, want := r.FromInventoryName("google", "compute.googleapis.com/Address", "//compute.googleapis.com/projects/p/global/addresses/a"), []string{"google_compute_global_address"}; !slices.Equal(got, want) {
		t.Errorf("a global address: %v, want %v", got, want)
	}
	if got, want := r.FromInventoryName("google", "compute.googleapis.com/Address", "//compute.googleapis.com/projects/p/regions/r/addresses/a"), []string{"google_compute_address"}; !slices.Equal(got, want) {
		t.Errorf("a regional address: %v, want %v", got, want)
	}
}

func TestFromARN(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for arn, want := range map[string][]string{
		"arn:aws:iam::123456789012:role/admin":                     {"aws_iam_role"},
		"arn:aws:iam::123456789012:role/service-role/app":          {"aws_iam_role"},
		"arn:aws:iam::123456789012:user/alice":                     {"aws_iam_user"},
		"arn:aws:acm:eu-west-2:123456789012:certificate/0123-4567": {"aws_acm_certificate"},
		"arn:aws:ec2:eu-west-2:123456789012:instance/i-0abc":       {"aws_instance"},
		"arn:aws:iam::123456789012:nonexistent/thing":              nil,
		"arn:aws:nowhere:eu-west-2:123456789012:instance/i-0abc":   nil,
		"not an arn":                 nil,
		"arn:aws:iam::123456789012:": nil,
		"":                           nil,
	} {
		if got := r.FromARN(arn); !slices.Equal(got, want) {
			t.Errorf("FromARN(%q) = %v, want %v", arn, got, want)
		}
	}
}

func TestCanonical(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	for typ, want := range map[string]string{
		"aws_alb":                       "aws_lb",
		"aws_lb":                        "aws_lb",
		"azurerm_linux_virtual_machine": "azurerm_virtual_machine",
		"aws_s3_bucket":                 "aws_s3_bucket",
		"not_a_type":                    "not_a_type",
	} {
		if got := r.Canonical(typ); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestChildren(t *testing.T) {
	t.Parallel()
	r := resolver(t)
	got := r.Children("aws_s3_bucket")
	if !slices.Contains(got, "aws_s3_bucket_versioning") {
		t.Errorf("Children(aws_s3_bucket) = %v, want its versioning among them", got)
	}
	if got := r.Children("unknown_type"); got != nil {
		t.Errorf("Children(unknown_type) = %v, want none", got)
	}
	// What a Resolver returns is the caller's: changing it changes nothing.
	got[0] = "changed"
	if again := r.Children("aws_s3_bucket"); again[0] == "changed" {
		t.Error("Children returned the catalogue's own slice")
	}
}

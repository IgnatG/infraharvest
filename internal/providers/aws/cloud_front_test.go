// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister names a distribution after its alias or comment, and lists
// only the account's own cache policies.
func TestCloudFrontListsDistributionsAndCustomPolicies(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch {
		case strings.HasSuffix(call.Path, "/distribution"):
			return `<DistributionList><IsTruncated>false</IsTruncated><Items>
				<DistributionSummary><Id>E1</Id><Comment>shop</Comment></DistributionSummary>
				<DistributionSummary><Id>E2</Id><Comment></Comment><Aliases><Quantity>1</Quantity><Items><CNAME>cdn.example.com</CNAME></Items></Aliases></DistributionSummary>
				<DistributionSummary><Id>E3</Id></DistributionSummary>
				</Items></DistributionList>`
		case strings.HasSuffix(call.Path, "/cache-policy"):
			if call.Query.Get("Type") != "custom" {
				t.Errorf("cache policies of type %q, want custom", call.Query.Get("Type"))
			}
			return `<CachePolicyList><Items><CachePolicySummary><Type>custom</Type><CachePolicy><Id>p1</Id></CachePolicy></CachePolicySummary></Items></CachePolicyList>`
		}
		t.Errorf("unexpected call %s?%s", call.Path, call.Query.Encode())
		return `<Error><Code>NotFound</Code></Error>`
	})
	g := &CloudFrontLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_cloudfront_distribution", "E1", "E2", "E3")
	assertIDs(t, resources, "aws_cloudfront_cache_policy", "p1")
	var names []string
	for _, r := range resources {
		if r.Address.Type == "aws_cloudfront_distribution" {
			names = append(names, r.RawName)
		}
	}
	if got := strings.Join(names, " "); got != "shop cdn.example.com E3" {
		t.Errorf("distributions named %s", got)
	}
}

// The cache policies are followed across pages by their marker, and a page
// that returns the marker it was asked with fails the listing rather than
// being asked for again forever.
func TestCloudFrontCachePolicyPages(t *testing.T) {
	t.Parallel()
	for _, repeat := range []bool{false, true} {
		calls := 0
		ctx := useFakeAPI(t, func(call apiCall) string {
			switch {
			case strings.HasSuffix(call.Path, "/distribution"):
				return `<DistributionList><IsTruncated>false</IsTruncated></DistributionList>`
			case strings.HasSuffix(call.Path, "/cache-policy"):
				if calls++; calls > 3 {
					t.Fatal("the cache policy pages are asked for forever")
				}
				policy := func(id string) string {
					return `<CachePolicySummary><Type>custom</Type><CachePolicy><Id>` + id + `</Id></CachePolicy></CachePolicySummary>`
				}
				switch marker := call.Query.Get("Marker"); {
				case marker == "":
					return `<CachePolicyList><NextMarker>m1</NextMarker><Items>` + policy("p1") + `</Items></CachePolicyList>`
				case marker == "m1" && repeat:
					return `<CachePolicyList><NextMarker>m1</NextMarker><Items>` + policy("p2") + `</Items></CachePolicyList>`
				case marker == "m1":
					return `<CachePolicyList><Items>` + policy("p2") + `</Items></CachePolicyList>`
				}
			}
			t.Errorf("unexpected call %s?%s", call.Path, call.Query.Encode())
			return `<Error><Code>NotFound</Code></Error>`
		})
		g := &CloudFrontLister{}

		resources, err := listChecked(ctx, t, g, harvest.Settings{})

		if repeat {
			if err == nil || !strings.Contains(err.Error(), "did not advance") {
				t.Errorf("a repeated marker: got %v, want the listing to fail", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		assertIDs(t, resources, "aws_cloudfront_cache_policy", "p1", "p2")
	}
}

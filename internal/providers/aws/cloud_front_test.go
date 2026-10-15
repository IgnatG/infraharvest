// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

// The lister names a distribution after its alias or comment, and lists
// only the account's own cache policies.
func TestCloudFrontListsDistributionsAndCustomPolicies(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
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

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_cloudfront_distribution", "E1", "E2", "E3")
	assertIDs(t, g.Resources, "aws_cloudfront_cache_policy", "p1")
	var names []string
	for _, r := range g.Resources {
		if r.Address.Type == "aws_cloudfront_distribution" {
			names = append(names, r.RawName)
		}
	}
	if got := strings.Join(names, " "); got != "shop cdn.example.com E3" {
		t.Errorf("distributions named %s", got)
	}
}

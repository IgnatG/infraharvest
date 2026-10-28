// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

func recordXraySamplingRule(name string, priority int) string {
	return fmt.Sprintf(`{"SamplingRule":{"RuleName":%q,"RuleARN":"arn:aws:xray:us-east-1:123456789012:sampling-rule/%s",
		"Priority":%d,"FixedRate":0.05,"ReservoirSize":1,"ServiceName":"*","ServiceType":"*","Host":"*","HTTPMethod":"*",
		"URLPath":"*","ResourceARN":"*","Version":1}}`, name, name, priority)
}

// The lister lists sampling rules by name, across pages, but the built-in
// Default rule, which can't be deleted.
func TestRecordXrayListsSamplingRules(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Path != "/GetSamplingRules" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"SamplingRuleRecords":[` + recordXraySamplingRule("checkout", 20) + `]}`
		}
		return `{"SamplingRuleRecords":[` + recordXraySamplingRule("Default", 10000) + `,` + recordXraySamplingRule("api", 10) + `],
			"NextToken":"` + nextPageToken + `"}`
	})
	g := &XrayLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_xray_sampling_rule", "api", "api", nil},
		recordedResource{"aws_xray_sampling_rule", "checkout", "checkout", nil},
	)
}

func TestRecordXrayReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("ThrottledException")
	})
	g := &XrayLister{}

	assertRecordedError(t, listError(ctx, g), "ThrottledException")
}

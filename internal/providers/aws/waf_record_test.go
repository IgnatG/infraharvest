// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// recordWafOps are the WAF Classic list operations, in the order the WAF and
// WAF Regional listers call them, each with the JSON keys of its result list
// and item ID and the Terraform type it lists, less the aws_waf_ or
// aws_wafregional_ prefix. Both APIs have the same operations and shapes.
var recordWafOps = []struct{ op, list, id, typ string }{
	{"ListWebACLs", "WebACLs", "WebACLId", "web_acl"},
	{"ListByteMatchSets", "ByteMatchSets", "ByteMatchSetId", "byte_match_set"},
	{"ListGeoMatchSets", "GeoMatchSets", "GeoMatchSetId", "geo_match_set"},
	{"ListIPSets", "IPSets", "IPSetId", "ipset"},
	{"ListRateBasedRules", "Rules", "RuleId", "rate_based_rule"},
	{"ListRegexMatchSets", "RegexMatchSets", "RegexMatchSetId", "regex_match_set"},
	{"ListRegexPatternSets", "RegexPatternSets", "RegexPatternSetId", "regex_pattern_set"},
	{"ListRules", "Rules", "RuleId", "rule"},
	{"ListRuleGroups", "RuleGroups", "RuleGroupId", "rule_group"},
	{"ListSizeConstraintSets", "SizeConstraintSets", "SizeConstraintSetId", "size_constraint_set"},
	{"ListSqlInjectionMatchSets", "SqlInjectionMatchSets", "SqlInjectionMatchSetId", "sql_injection_match_set"},
	{"ListXssMatchSets", "XssMatchSets", "XssMatchSetId", "xss_match_set"},
}

// recordWafID is the ID the fake WAF API gives the item on page of the i-th
// operation of recordWafOps, 36 characters like AWS's. Its first eight
// characters are "<i>-<page>-id0" with i in two digits: "03-2-id0" is the
// IP set on the second page.
func recordWafID(i, page int) string {
	return fmt.Sprintf("%02d-%d-id0-1111-4222-8333-444455556666", i, page)
}

// recordWafAPI answers every WAF Classic list operation with one item on
// each of two pages, the first named "main" and the second "backup".
func recordWafAPI(t *testing.T) func(apiCall) string {
	return func(call apiCall) string {
		for i, o := range recordWafOps {
			if call.Op != o.op {
				continue
			}
			if recordSecondPage(call) {
				return fmt.Sprintf(`{%q:[{%q:%q,"Name":"backup"}]}`, o.list, o.id, recordWafID(i, 2))
			}
			return fmt.Sprintf(`{%q:[{%q:%q,"Name":"main"}],"NextMarker":%q}`, o.list, o.id, recordWafID(i, 1), nextPageToken)
		}
		return recordUnexpected(t, call)
	}
}

// recordedWaf is what a lister lists from recordWafAPI, with each type
// prefixed by prefix: every operation's two pages, in the order of
// recordWafOps. Each item is listed by ID, named by its name, an underscore
// and the first eight characters of its ID.
func recordedWaf(prefix string) []recordedResource {
	var want []recordedResource
	for i, o := range recordWafOps {
		first, second := recordWafID(i, 1), recordWafID(i, 2)
		want = append(want,
			recordedResource{prefix + o.typ, first, "main_" + first[:8], nil},
			recordedResource{prefix + o.typ, second, "backup_" + second[:8], nil},
		)
	}
	return want
}

// assertRecordedWaf checks that resources are what recordWafAPI answers
// (see recordedWaf).
func assertRecordedWaf(t *testing.T, resources []harvest.Resource, prefix string) {
	t.Helper()
	assertRecorded(t, resources, recordedWaf(prefix)...)
}

// recordWafErrorAPI fails the operation failing and answers the operations
// before it with no items. An operation after it is unexpected: the lister
// stops at the first error.
func recordWafErrorAPI(t *testing.T, failing string) func(apiCall) string {
	failed := false
	return func(call apiCall) string {
		if failed {
			return recordUnexpected(t, call)
		}
		if call.Op == failing {
			failed = true
			return recordJSONError("WAFInternalErrorException")
		}
		for _, o := range recordWafOps {
			if call.Op == o.op {
				return fmt.Sprintf(`{%q:[]}`, o.list)
			}
		}
		return recordUnexpected(t, call)
	}
}

// recordWafRepeatedMarkerAPI answers ListWebACLs with a marker, and with the
// same marker again when asked for it. Other operations are unexpected; at
// most three calls are answered, so a lister that loops ends.
func recordWafRepeatedMarkerAPI(t *testing.T) func(apiCall) string {
	calls := 0
	return func(call apiCall) string {
		calls++
		if call.Op != "ListWebACLs" || calls > 3 {
			return recordUnexpected(t, call)
		}
		return fmt.Sprintf(`{"WebACLs":[{"WebACLId":%q,"Name":"main"}],"NextMarker":%q}`, recordWafID(calls, 1), nextPageToken)
	}
}

// recordWafShortIDAPI answers ListWebACLs with a web ACL whose ID is shorter
// than eight characters and one whose ID is eight characters long, and
// every other list operation, and ListResourcesForWebACL, with no items.
func recordWafShortIDAPI(t *testing.T) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == "ListResourcesForWebACL" {
			return `{"ResourceArns":[]}`
		}
		for _, o := range recordWafOps {
			if call.Op != o.op {
				continue
			}
			if o.op == "ListWebACLs" {
				return `{"WebACLs":[{"WebACLId":"short","Name":"main"},{"WebACLId":"abcdefgh","Name":"edge"}]}`
			}
			return fmt.Sprintf(`{%q:[]}`, o.list)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists, in turn, the web ACLs, byte match sets, geo match sets,
// IP sets, rate-based rules, regex match sets, regex pattern sets, rules,
// rule groups, size constraint sets, SQL injection match sets and XSS match
// sets of WAF Classic, each across pages.
func TestRecordWafListsEveryType(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordWafAPI(t))
	g := &WafLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecordedWaf(t, resources, "aws_waf_")
}

// An error from any list operation is returned, and the operations after it
// are not called.
func TestRecordWafReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, o := range recordWafOps {
		t.Run(o.op, func(t *testing.T) {
			ctx := useFakeAPI(t, recordWafErrorAPI(t, o.op))
			g := &WafLister{}

			assertRecordedError(t, listError(ctx, g), "WAFInternalErrorException")
		})
	}
}

// A page that sends back the marker it was asked with is an error, not an
// endless listing.
func TestRecordWafReturnsRepeatedMarkerError(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordWafRepeatedMarkerAPI(t))
	g := &WafLister{}

	assertRecordedError(t, listError(ctx, g), "pagination did not advance")
}

// An ID of eight characters or fewer ends the name whole. AWS gives
// 36-character IDs.
func TestRecordWafNamesShortIDs(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordWafShortIDAPI(t))
	g := &WafLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_waf_web_acl", "short", "main_short", nil},
		recordedResource{"aws_waf_web_acl", "abcdefgh", "edge_abcdefgh", nil},
	)
}

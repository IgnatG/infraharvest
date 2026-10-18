// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/waf"
	"github.com/aws/aws-sdk-go-v2/service/waf/types"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// WAF Classic comes as two APIs with the same operations and shapes: a
// global one for CloudFront (waf) and a regional one (wafregional). Their
// SDK types differ only by package, so each lister turns its API's answers
// into wafClassicItems and leaves the paging and naming to listWafClassic.
// WAF Classic has reached end of support, but what still exists can be
// imported.

// wafClassicItem is one item a WAF Classic list operation returns.
type wafClassicItem struct {
	id, name string
}

// wafClassicList is one WAF Classic list operation: the Terraform type it
// lists, and listPage, which asks for the page at marker (nil for the
// first) and returns its items and the marker of the next page.
type wafClassicList struct {
	resourceType string
	listPage     func(ctx context.Context, marker *string) (items []wafClassicItem, nextMarker *string, err error)
}

// wafClassicItems turns the summaries on a page into items, with fields
// picking a summary's ID and name.
func wafClassicItems[S any](summaries []S, fields func(S) (id, name *string)) []wafClassicItem {
	items := make([]wafClassicItem, 0, len(summaries))
	for _, summary := range summaries {
		id, name := fields(summary)
		items = append(items, wafClassicItem{id: aws.ToString(id), name: aws.ToString(name)})
	}
	return items
}

// listWafClassic calls the list operations one after another, paging each
// to its end before the next starts, and returns what they list by ID,
// which is what Terraform imports them with. The first error is returned
// at once, with nothing listed. A page that sends back the marker it was
// asked with is such an error, which paginateByMarker reports.
//
// Names are not unique in WAF Classic, so each item is named by
// wafClassicName.
func listWafClassic(ctx context.Context, lists []wafClassicList) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	for _, list := range lists {
		err := paginateByMarker(func(marker *string) (*string, error) {
			items, next, err := list.listPage(ctx, marker)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				resources = append(resources, harvest.RecordBare(item.id, wafClassicName(item.name, item.id), list.resourceType, "aws"))
			}
			return next, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return resources, nil
}

// wafClassicIDPrefix is how many characters of an item's ID its name ends
// with.
const wafClassicIDPrefix = 8

// wafClassicName is the name of a WAF Classic item: its name, an
// underscore and the first eight characters of its ID, or the whole ID
// when it is shorter. AWS gives 36-character IDs, so the eight characters
// tell items of the same name apart.
func wafClassicName(name, id string) string {
	if len(id) > wafClassicIDPrefix {
		id = id[:wafClassicIDPrefix]
	}
	return name + "_" + id
}

// WafLister lists the global WAF Classic resources, the ones CloudFront
// distributions use, as aws_waf_ types. It runs once, not per region.
type WafLister struct {
	Service
}

// List lists, in turn, the web ACLs, byte match sets, geo match
// sets, IP sets, rate-based rules, regex match sets, regex pattern sets,
// rules, rule groups, size constraint sets, SQL injection match sets and
// XSS match sets, and returns the first error.
func (g *WafLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	resources, err := listWafClassic(g.Context(), wafLists(waf.NewFromConfig(config)))
	if err != nil {
		return err
	}
	g.Resources = resources
	return nil
}

// wafRule picks a rule's ID and name. Rate-based rules and ordinary rules
// share the summary type, though Terraform has a type for each.
func wafRule(s types.RuleSummary) (id, name *string) { return s.RuleId, s.Name }

// wafLists are the global WAF Classic list operations, in the order they
// are called.
func wafLists(svc *waf.Client) []wafClassicList {
	return []wafClassicList{
		{"aws_waf_web_acl", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListWebACLs(ctx, &waf.ListWebACLsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.WebACLs, func(s types.WebACLSummary) (id, name *string) { return s.WebACLId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_byte_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListByteMatchSets(ctx, &waf.ListByteMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.ByteMatchSets, func(s types.ByteMatchSetSummary) (id, name *string) { return s.ByteMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_geo_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListGeoMatchSets(ctx, &waf.ListGeoMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.GeoMatchSets, func(s types.GeoMatchSetSummary) (id, name *string) { return s.GeoMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_ipset", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListIPSets(ctx, &waf.ListIPSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.IPSets, func(s types.IPSetSummary) (id, name *string) { return s.IPSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_rate_based_rule", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRateBasedRules(ctx, &waf.ListRateBasedRulesInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.Rules, wafRule), out.NextMarker, nil
		}},
		{"aws_waf_regex_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRegexMatchSets(ctx, &waf.ListRegexMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.RegexMatchSets, func(s types.RegexMatchSetSummary) (id, name *string) { return s.RegexMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_regex_pattern_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRegexPatternSets(ctx, &waf.ListRegexPatternSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.RegexPatternSets, func(s types.RegexPatternSetSummary) (id, name *string) { return s.RegexPatternSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_rule", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRules(ctx, &waf.ListRulesInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.Rules, wafRule), out.NextMarker, nil
		}},
		{"aws_waf_rule_group", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRuleGroups(ctx, &waf.ListRuleGroupsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.RuleGroups, func(s types.RuleGroupSummary) (id, name *string) { return s.RuleGroupId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_size_constraint_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListSizeConstraintSets(ctx, &waf.ListSizeConstraintSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.SizeConstraintSets, func(s types.SizeConstraintSetSummary) (id, name *string) { return s.SizeConstraintSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_sql_injection_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListSqlInjectionMatchSets(ctx, &waf.ListSqlInjectionMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.SqlInjectionMatchSets, func(s types.SqlInjectionMatchSetSummary) (id, name *string) { return s.SqlInjectionMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_waf_xss_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListXssMatchSets(ctx, &waf.ListXssMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.XssMatchSets, func(s types.XssMatchSetSummary) (id, name *string) { return s.XssMatchSetId, s.Name }), out.NextMarker, nil
		}},
	}
}

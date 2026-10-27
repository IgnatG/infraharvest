// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/wafregional"
	"github.com/aws/aws-sdk-go-v2/service/wafregional/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// WafRegionalLister lists a region's WAF Classic resources, the ones
// load balancers and API Gateway stages use, as aws_wafregional_ types. It
// lists the same twelve types as WafLister, the same way (see
// listWafClassic), and then which resources each web ACL protects.
type WafRegionalLister struct {
	Service
}

// List lists, in turn, the web ACLs, byte match sets, geo match
// sets, IP sets, rate-based rules, regex match sets, regex pattern sets,
// rules, rule groups, size constraint sets, SQL injection match sets and
// XSS match sets, then the associations of each web ACL, and returns the
// first error.
func (g *WafRegionalLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := wafregional.NewFromConfig(config)
	resources, err := listWafClassic(g.Context(), wafRegionalLists(svc))
	if err != nil {
		return err
	}
	var associations []harvest.Resource
	for _, acl := range resources {
		// A call per web ACL and resource type, for associations the
		// import may not keep.
		if acl.Address.Type != "aws_wafregional_web_acl" || !g.wants("aws_wafregional_web_acl_association") {
			continue
		}
		protected, err := g.webACLAssociations(svc, acl.Observed.ID, acl.RawName)
		if err != nil {
			return err
		}
		associations = append(associations, protected...)
	}
	g.Resources = slices.Concat(resources, associations)
	return nil
}

// webACLAssociations lists the resources the web ACL with this ID and
// listed name protects, one resource type at a time, for every type the
// API takes: load balancers and API Gateway stages. A region that doesn't
// offer a type rejects the call with WAFInvalidParameterException, and a
// web ACL deleted since it was listed with WAFNonexistentItemException, so
// those errors only skip the type; any other error is returned. Terraform
// imports an association as <web ACL ID>:<resource ARN>. Each is named by
// the web ACL's listed name, an underscore and the protected resource's
// name (see protectedName).
func (g *WafRegionalLister) webACLAssociations(svc *wafregional.Client, aclID, aclName string) ([]harvest.Resource, error) {
	var associations []harvest.Resource
	for _, resourceType := range types.ResourceType("").Values() {
		out, err := svc.ListResourcesForWebACL(g.Context(), &wafregional.ListResourcesForWebACLInput{
			WebACLId:     aws.String(aclID),
			ResourceType: resourceType,
		})
		var invalidParameter *types.WAFInvalidParameterException
		var nonexistent *types.WAFNonexistentItemException
		if errors.As(err, &invalidParameter) || errors.As(err, &nonexistent) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, resourceARN := range out.ResourceArns {
			associations = append(associations, harvest.Record(
				aclID+":"+resourceARN,
				aclName+"_"+protectedName(resourceARN),
				"aws_wafregional_web_acl_association",
				"aws",
				map[string]string{
					"resource_arn": resourceARN,
					"web_acl_id":   aclID,
				},
			))
		}
	}
	return associations, nil
}

// wafRegionalRule picks a rule's ID and name. Rate-based rules and
// ordinary rules share the summary type, though Terraform has a type for
// each.
func wafRegionalRule(s types.RuleSummary) (id, name *string) { return s.RuleId, s.Name }

// wafRegionalLists are the regional WAF Classic list operations, in the
// order they are called.
func wafRegionalLists(svc *wafregional.Client) []wafClassicList {
	return []wafClassicList{
		{"aws_wafregional_web_acl", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListWebACLs(ctx, &wafregional.ListWebACLsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.WebACLs, func(s types.WebACLSummary) (id, name *string) { return s.WebACLId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_byte_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListByteMatchSets(ctx, &wafregional.ListByteMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.ByteMatchSets, func(s types.ByteMatchSetSummary) (id, name *string) { return s.ByteMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_geo_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListGeoMatchSets(ctx, &wafregional.ListGeoMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.GeoMatchSets, func(s types.GeoMatchSetSummary) (id, name *string) { return s.GeoMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_ipset", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListIPSets(ctx, &wafregional.ListIPSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.IPSets, func(s types.IPSetSummary) (id, name *string) { return s.IPSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_rate_based_rule", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRateBasedRules(ctx, &wafregional.ListRateBasedRulesInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.Rules, wafRegionalRule), out.NextMarker, nil
		}},
		{"aws_wafregional_regex_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRegexMatchSets(ctx, &wafregional.ListRegexMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.RegexMatchSets, func(s types.RegexMatchSetSummary) (id, name *string) { return s.RegexMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_regex_pattern_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRegexPatternSets(ctx, &wafregional.ListRegexPatternSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.RegexPatternSets, func(s types.RegexPatternSetSummary) (id, name *string) { return s.RegexPatternSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_rule", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRules(ctx, &wafregional.ListRulesInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.Rules, wafRegionalRule), out.NextMarker, nil
		}},
		{"aws_wafregional_rule_group", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListRuleGroups(ctx, &wafregional.ListRuleGroupsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.RuleGroups, func(s types.RuleGroupSummary) (id, name *string) { return s.RuleGroupId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_size_constraint_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListSizeConstraintSets(ctx, &wafregional.ListSizeConstraintSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.SizeConstraintSets, func(s types.SizeConstraintSetSummary) (id, name *string) { return s.SizeConstraintSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_sql_injection_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListSqlInjectionMatchSets(ctx, &wafregional.ListSqlInjectionMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.SqlInjectionMatchSets, func(s types.SqlInjectionMatchSetSummary) (id, name *string) { return s.SqlInjectionMatchSetId, s.Name }), out.NextMarker, nil
		}},
		{"aws_wafregional_xss_match_set", func(ctx context.Context, marker *string) ([]wafClassicItem, *string, error) {
			out, err := svc.ListXssMatchSets(ctx, &wafregional.ListXssMatchSetsInput{NextMarker: marker})
			if err != nil {
				return nil, nil, err
			}
			return wafClassicItems(out.XssMatchSets, func(s types.XssMatchSetSummary) (id, name *string) { return s.XssMatchSetId, s.Name }), out.NextMarker, nil
		}},
	}
}

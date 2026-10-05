// Copyright 2020 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/waf"
)

type WafGenerator struct {
	AWSService
}

func (g *WafGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := waf.NewFromConfig(config)

	if err := g.loadWebACL(svc); err != nil {
		return err
	}
	if err := g.loadByteMatchSet(svc); err != nil {
		return err
	}
	if err := g.loadGeoMatchSet(svc); err != nil {
		return err
	}
	if err := g.loadIPSet(svc); err != nil {
		return err
	}
	if err := g.loadRateBasedRules(svc); err != nil {
		return err
	}
	if err := g.loadRegexMatchSets(svc); err != nil {
		return err
	}
	if err := g.loadRegexPatternSets(svc); err != nil {
		return err
	}
	if err := g.loadWafRules(svc); err != nil {
		return err
	}
	if err := g.loadWafRuleGroups(svc); err != nil {
		return err
	}
	if err := g.loadSizeConstraintSets(svc); err != nil {
		return err
	}
	if err := g.loadSQLInjectionMatchSets(svc); err != nil {
		return err
	}
	if err := g.loadXSSMatchSet(svc); err != nil {
		return err
	}

	return nil
}

func (g *WafGenerator) loadWebACL(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListWebACLs(g.Context(), &waf.ListWebACLsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, acl := range output.WebACLs {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*acl.WebACLId,
				*acl.Name+"_"+(*acl.WebACLId)[0:8],
				"aws_waf_web_acl",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadByteMatchSet(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListByteMatchSets(g.Context(), &waf.ListByteMatchSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, byteMatchSet := range output.ByteMatchSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*byteMatchSet.ByteMatchSetId,
				*byteMatchSet.Name+"_"+(*byteMatchSet.ByteMatchSetId)[0:8],
				"aws_waf_byte_match_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadGeoMatchSet(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListGeoMatchSets(g.Context(), &waf.ListGeoMatchSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, matchSet := range output.GeoMatchSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*matchSet.GeoMatchSetId,
				*matchSet.Name+"_"+(*matchSet.GeoMatchSetId)[0:8],
				"aws_waf_geo_match_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadIPSet(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListIPSets(g.Context(), &waf.ListIPSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, IPSet := range output.IPSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*IPSet.IPSetId,
				*IPSet.Name+"_"+(*IPSet.IPSetId)[0:8],
				"aws_waf_ipset",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadRateBasedRules(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRateBasedRules(g.Context(), &waf.ListRateBasedRulesInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, rule := range output.Rules {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*rule.RuleId,
				*rule.Name+"_"+(*rule.RuleId)[0:8],
				"aws_waf_rate_based_rule",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadRegexMatchSets(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRegexMatchSets(g.Context(), &waf.ListRegexMatchSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, regexMatchSet := range output.RegexMatchSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*regexMatchSet.RegexMatchSetId,
				*regexMatchSet.Name+"_"+(*regexMatchSet.RegexMatchSetId)[0:8],
				"aws_waf_regex_match_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadRegexPatternSets(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRegexPatternSets(g.Context(), &waf.ListRegexPatternSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, regexPatternSet := range output.RegexPatternSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*regexPatternSet.RegexPatternSetId,
				*regexPatternSet.Name+"_"+(*regexPatternSet.RegexPatternSetId)[0:8],
				"aws_waf_regex_pattern_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadWafRules(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRules(g.Context(), &waf.ListRulesInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, rule := range output.Rules {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*rule.RuleId,
				*rule.Name+"_"+(*rule.RuleId)[0:8],
				"aws_waf_rule",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadWafRuleGroups(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRuleGroups(g.Context(), &waf.ListRuleGroupsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, ruleGroup := range output.RuleGroups {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*ruleGroup.RuleGroupId,
				*ruleGroup.Name+"_"+(*ruleGroup.RuleGroupId)[0:8],
				"aws_waf_rule_group",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadSizeConstraintSets(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListSizeConstraintSets(g.Context(), &waf.ListSizeConstraintSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, sizeConstraintSet := range output.SizeConstraintSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*sizeConstraintSet.SizeConstraintSetId,
				*sizeConstraintSet.Name+"_"+(*sizeConstraintSet.SizeConstraintSetId)[0:8],
				"aws_waf_size_constraint_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadSQLInjectionMatchSets(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListSqlInjectionMatchSets(g.Context(), &waf.ListSqlInjectionMatchSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, sqlInjectionMatchSet := range output.SqlInjectionMatchSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*sqlInjectionMatchSet.SqlInjectionMatchSetId,
				*sqlInjectionMatchSet.Name+"_"+(*sqlInjectionMatchSet.SqlInjectionMatchSetId)[0:8],
				"aws_waf_sql_injection_match_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

func (g *WafGenerator) loadXSSMatchSet(svc *waf.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListXssMatchSets(g.Context(), &waf.ListXssMatchSetsInput{NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, xssMatchSet := range output.XssMatchSets {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*xssMatchSet.XssMatchSetId,
				*xssMatchSet.Name+"_"+(*xssMatchSet.XssMatchSetId)[0:8],
				"aws_waf_xss_match_set",
				"aws"))
		}
		return output.NextMarker, nil
	})
}

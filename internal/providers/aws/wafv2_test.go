// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

// The lister records the IDs Terraform imports WAFv2 resources with: ID,
// name and scope, and an association as the web ACL's ARN and the
// protected resource's.
func TestWafv2ListsWithImportIDs(t *testing.T) {
	const aclARN = "arn:aws:wafv2:eu-west-2:123456789012:regional/webacl/edge/a1"
	const lbARN = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/web/50dc6c495c0c9188"
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListWebACLs":
			return `{"WebACLs":[{"Id":"a1","Name":"edge","ARN":"` + aclARN + `"}]}`
		case "ListResourcesForWebACL":
			if strings.Contains(call.Body, `"AGENTCORE_GATEWAY"`) {
				t.Error("asked about AgentCore gateways, whose associations Terraform can't import")
			}
			if strings.Contains(call.Body, `"APPLICATION_LOAD_BALANCER"`) {
				return `{"ResourceArns":["` + lbARN + `"]}`
			}
			return `{"ResourceArns":[]}`
		case "ListIPSets":
			return `{"IPSets":[{"Id":"i1","Name":"office"}]}`
		case "ListRegexPatternSets":
			return `{"RegexPatternSets":[]}`
		case "ListRuleGroups":
			return `{"RuleGroups":[{"Id":"g1","Name":"bots","ARN":"arn:group"}]}`
		case "ListLoggingConfigurations":
			return `{"LoggingConfigurations":[]}`
		}
		t.Errorf("unexpected call %s", call.Op)
		return `{}`
	})
	g := NewWafv2RegionalLister()

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_wafv2_web_acl", "a1/edge/REGIONAL")
	assertIDs(t, g.Resources, "aws_wafv2_web_acl_association", aclARN+","+lbARN)
	assertIDs(t, g.Resources, "aws_wafv2_ip_set", "i1/office/REGIONAL")
	assertIDs(t, g.Resources, "aws_wafv2_rule_group", "g1/bots/REGIONAL")
	for _, r := range g.Resources {
		if r.Address.Type == "aws_wafv2_web_acl_association" && r.RawName != "edge_web" {
			t.Errorf("association named %q, want edge_web", r.RawName)
		}
	}
}

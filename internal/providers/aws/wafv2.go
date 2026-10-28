// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	"github.com/aws/aws-sdk-go-v2/service/wafv2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Wafv2Lister lists the WAFv2 resources of one scope: web ACLs, IP sets,
// regex pattern sets, rule groups and logging configurations, and, in the
// regional scope, which resources each web ACL protects. The CLOUDFRONT
// scope only exists in us-east-1, so wafv2_cloudfront is listed there
// only; wafv2_regional is listed in every region.
type Wafv2Lister struct {
	Service
	scope types.Scope
}

// NewWafv2CloudfrontLister returns the lister of the WAFv2 resources
// that protect CloudFront distributions.
func NewWafv2CloudfrontLister() *Wafv2Lister {
	return &Wafv2Lister{scope: types.ScopeCloudfront}
}

// NewWafv2RegionalLister returns the lister of the region's WAFv2
// resources.
func NewWafv2RegionalLister() *Wafv2Lister {
	return &Wafv2Lister{scope: types.ScopeRegional}
}

// associatedTypes are the types of resource whose web ACL associations are
// listed: those Terraform's aws_wafv2_web_acl_association can import. Each
// type needs a ListResourcesForWebACL call of its own.
var associatedTypes = []types.ResourceType{
	types.ResourceTypeApplicationLoadBalancer,
	types.ResourceTypeApiGateway,
	types.ResourceTypeAppsync,
	types.ResourceTypeCognitioUserPool,
	types.ResourceTypeAppRunnerService,
	types.ResourceTypeVerifiedAccessInstance,
}

// wafv2ImportID is the ID Terraform imports a web ACL, IP set, regex
// pattern set or rule group with: <ID>/<name>/<scope>.
func wafv2ImportID(id, name *string, scope types.Scope) string {
	return aws.ToString(id) + "/" + aws.ToString(name) + "/" + string(scope)
}

// List lists the web ACLs (with their associations), IP sets,
// regex pattern sets, rule groups and logging configurations, in that
// order. Any error stops the listing and is returned.
func (g *Wafv2Lister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *Wafv2Lister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := wafv2.NewFromConfig(config)

	for _, list := range []func(*wafv2.Client) error{
		g.listWebACLs,
		g.listIPSets,
		g.listRegexPatternSets,
		g.listRuleGroups,
		g.listLoggingConfigurations,
	} {
		if err := list(svc); err != nil {
			return err
		}
	}
	return nil
}

// listWebACLs lists the web ACLs, each followed, in the regional scope, by
// its associations. The SDK has no paginators for WAFv2, so this and every
// other list follows NextMarker with paginateByMarker.
func (g *Wafv2Lister) listWebACLs(svc *wafv2.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListWebACLs(g.Context(), &wafv2.ListWebACLsInput{Scope: g.scope, NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, acl := range output.WebACLs {
			g.Resources = append(g.Resources, harvest.Record(
				wafv2ImportID(acl.Id, acl.Name, g.scope),
				aws.ToString(acl.Name),
				"aws_wafv2_web_acl",
				"aws",
				map[string]string{
					"name":  aws.ToString(acl.Name),
					"scope": string(g.scope),
				},
			))
			// A CloudFront distribution names its web ACL itself, so
			// only regional web ACLs have associations: a call per
			// resource type, for associations the import may not keep.
			if g.scope == types.ScopeRegional && g.Wants("aws_wafv2_web_acl_association") {
				if err := g.listWebACLAssociations(svc, acl); err != nil {
					return nil, err
				}
			}
		}
		return output.NextMarker, nil
	})
}

// listWebACLAssociations lists the resources the web ACL protects, one
// resource type at a time. Not every region offers every type, and a
// region without one rejects the call with WAFInvalidParameterException
// (or WAFNonexistentItemException), so those errors only skip the type;
// any other error is returned. Terraform imports an association as
// <web ACL ARN>,<resource ARN>.
func (g *Wafv2Lister) listWebACLAssociations(svc *wafv2.Client, acl types.WebACLSummary) error {
	aclARN := aws.ToString(acl.ARN)
	for _, resourceType := range associatedTypes {
		output, err := svc.ListResourcesForWebACL(g.Context(), &wafv2.ListResourcesForWebACLInput{
			WebACLArn:    acl.ARN,
			ResourceType: resourceType,
		})
		var invalidParameter *types.WAFInvalidParameterException
		var nonexistent *types.WAFNonexistentItemException
		if errors.As(err, &invalidParameter) || errors.As(err, &nonexistent) {
			continue
		}
		if err != nil {
			return err
		}
		for _, resourceARN := range output.ResourceArns {
			g.Resources = append(g.Resources, harvest.Record(
				aclARN+","+resourceARN,
				aws.ToString(acl.Name)+"_"+protectedName(resourceARN),
				"aws_wafv2_web_acl_association",
				"aws",
				map[string]string{
					"resource_arn": resourceARN,
					"web_acl_arn":  aclARN,
				},
			))
		}
	}
	return nil
}

// protectedName is the name of a resource a web ACL protects, taken from
// its ARN: the last part of the ARN's resource, as in a stage's
// .../restapis/a1b2/stages/prod, except for a load balancer, whose resource
// is loadbalancer/<kind>/<name>/<ID> and which is named after its name.
func protectedName(arn string) string {
	resource := arn[strings.LastIndex(arn, ":")+1:]
	parts := strings.Split(resource, "/")
	if parts[0] == "loadbalancer" && len(parts) >= 3 {
		return parts[2]
	}
	return parts[len(parts)-1]
}

// listIPSets lists the IP sets.
func (g *Wafv2Lister) listIPSets(svc *wafv2.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListIPSets(g.Context(), &wafv2.ListIPSetsInput{Scope: g.scope, NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, set := range output.IPSets {
			g.Resources = append(g.Resources, harvest.Record(
				wafv2ImportID(set.Id, set.Name, g.scope),
				aws.ToString(set.Name),
				"aws_wafv2_ip_set",
				"aws",
				map[string]string{
					"name":  aws.ToString(set.Name),
					"scope": string(g.scope),
				},
			))
		}
		return output.NextMarker, nil
	})
}

// listRegexPatternSets lists the regex pattern sets.
func (g *Wafv2Lister) listRegexPatternSets(svc *wafv2.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRegexPatternSets(g.Context(), &wafv2.ListRegexPatternSetsInput{Scope: g.scope, NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, set := range output.RegexPatternSets {
			g.Resources = append(g.Resources, harvest.Record(
				wafv2ImportID(set.Id, set.Name, g.scope),
				aws.ToString(set.Name),
				"aws_wafv2_regex_pattern_set",
				"aws",
				map[string]string{
					"name":  aws.ToString(set.Name),
					"scope": string(g.scope),
				},
			))
		}
		return output.NextMarker, nil
	})
}

// listRuleGroups lists the account's own rule groups; the managed rule
// groups that vendors offer are listed by another call and left out.
func (g *Wafv2Lister) listRuleGroups(svc *wafv2.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListRuleGroups(g.Context(), &wafv2.ListRuleGroupsInput{Scope: g.scope, NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, group := range output.RuleGroups {
			g.Resources = append(g.Resources, harvest.Record(
				wafv2ImportID(group.Id, group.Name, g.scope),
				aws.ToString(group.Name),
				"aws_wafv2_rule_group",
				"aws",
				map[string]string{
					"arn":   aws.ToString(group.ARN),
					"name":  aws.ToString(group.Name),
					"scope": string(g.scope),
				},
			))
		}
		return output.NextMarker, nil
	})
}

// listLoggingConfigurations lists the web ACLs' logging
// configurations. Terraform imports one by its web ACL's ARN, which also
// names it.
func (g *Wafv2Lister) listLoggingConfigurations(svc *wafv2.Client) error {
	return paginateByMarker(func(marker *string) (*string, error) {
		output, err := svc.ListLoggingConfigurations(g.Context(), &wafv2.ListLoggingConfigurationsInput{Scope: g.scope, NextMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, logging := range output.LoggingConfigurations {
			resourceARN := aws.ToString(logging.ResourceArn)
			g.Resources = append(g.Resources, harvest.Record(
				resourceARN,
				resourceARN,
				"aws_wafv2_web_acl_logging_configuration",
				"aws",
				map[string]string{"resource_arn": resourceARN},
			))
		}
		return output.NextMarker, nil
	})
}

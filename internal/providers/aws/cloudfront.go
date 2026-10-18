// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// CloudFrontLister lists CloudFront distributions and the account's own
// cache policies. CloudFront is global, so the service is listed once, in
// the aws-global region.
type CloudFrontLister struct {
	Service
}

// List lists the distributions, then the cache policies. Any
// error stops the listing and is returned.
func (g *CloudFrontLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := cloudfront.NewFromConfig(config)

	if err := g.listDistributions(svc); err != nil {
		return err
	}
	return g.listCachePolicies(svc)
}

// listDistributions lists the distributions. Each records retain_on_delete
// as false, the provider's default.
func (g *CloudFrontLister) listDistributions(svc *cloudfront.Client) error {
	p := cloudfront.NewListDistributionsPaginator(svc, &cloudfront.ListDistributionsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		if page.DistributionList == nil {
			continue
		}
		for _, distribution := range page.DistributionList.Items {
			g.Resources = append(g.Resources, harvest.Record(
				aws.ToString(distribution.Id),
				distributionName(distribution),
				"aws_cloudfront_distribution",
				"aws",
				map[string]string{"retain_on_delete": "false"},
			))
		}
	}
	return nil
}

// distributionName names a distribution after what people know it by: its
// first alias (the domain it serves), else its comment, else its ID.
func distributionName(distribution types.DistributionSummary) string {
	if distribution.Aliases != nil && len(distribution.Aliases.Items) > 0 {
		return distribution.Aliases.Items[0]
	}
	if comment := aws.ToString(distribution.Comment); comment != "" {
		return comment
	}
	return aws.ToString(distribution.Id)
}

// listCachePolicies lists the cache policies the account made. The managed
// policies AWS provides to every account are left out by asking for custom
// ones only. The SDK has no paginator for ListCachePolicies, so the pages
// are followed by their marker until a page returns none.
func (g *CloudFrontLister) listCachePolicies(svc *cloudfront.Client) error {
	input := &cloudfront.ListCachePoliciesInput{Type: types.CachePolicyTypeCustom}
	for {
		output, err := svc.ListCachePolicies(g.Context(), input)
		if err != nil {
			return err
		}
		list := output.CachePolicyList
		if list == nil {
			return nil
		}
		for _, summary := range list.Items {
			if summary.CachePolicy == nil {
				continue
			}
			id := aws.ToString(summary.CachePolicy.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_cloudfront_cache_policy", "aws"))
		}
		if aws.ToString(list.NextMarker) == "" {
			return nil
		}
		input.Marker = list.NextMarker
	}
}

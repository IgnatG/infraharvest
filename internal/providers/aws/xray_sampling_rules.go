// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/xray"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// XrayLister lists the region's X-Ray sampling rules.
type XrayLister struct {
	Service
}

// List records every sampling rule, by rule name, which is what Terraform
// imports a rule by. The catalogue skips the built-in Default rule.
func (g *XrayLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *XrayLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	for page, err := range pagesOf(g.Context(), xray.NewGetSamplingRulesPaginator(xray.NewFromConfig(config), &xray.GetSamplingRulesInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, record := range page.SamplingRuleRecords {
			// A record always carries its rule; skip one that doesn't
			// rather than dereference nil.
			if record.SamplingRule == nil {
				continue
			}
			name := aws.ToString(record.SamplingRule.RuleName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_xray_sampling_rule", "aws"))
		}
	}
	return nil
}

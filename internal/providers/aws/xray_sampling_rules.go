// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/xray"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// xrayDefaultRule is the name of the sampling rule X-Ray creates in every
// region. It can't be deleted, so it isn't the account's to manage.
const xrayDefaultRule = "Default"

// XrayLister lists the region's X-Ray sampling rules.
type XrayLister struct {
	Service
}

// List records every sampling rule but the built-in default one,
// by rule name, which is what Terraform imports a rule by.
func (g *XrayLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *XrayLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	pages := xray.NewGetSamplingRulesPaginator(xray.NewFromConfig(config), &xray.GetSamplingRulesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
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
			if name == xrayDefaultRule {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_xray_sampling_rule", "aws"))
		}
	}
	return nil
}

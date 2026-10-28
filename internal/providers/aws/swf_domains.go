// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/swf"
	swftypes "github.com/aws/aws-sdk-go-v2/service/swf/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SWFLister lists the region's Simple Workflow Service domains.
type SWFLister struct {
	Service
}

// List records the registered domains, each by name, which is
// what Terraform imports a domain by. Deprecated domains are left out:
// deprecating a domain is how it is deleted, and Terraform reads a
// deprecated domain as gone.
func (g *SWFLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *SWFLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := swf.NewFromConfig(config)
	input := &swf.ListDomainsInput{RegistrationStatus: swftypes.RegistrationStatusRegistered}
	pages := swf.NewListDomainsPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, domain := range page.DomainInfos {
			name := aws.ToString(domain.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_swf_domain", "aws"))
		}
	}
	return nil
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SsmLister lists the region's Systems Manager parameters (the `ssm`
// service).
type SsmLister struct {
	Service
}

// List records every parameter, whatever its type, as an
// aws_ssm_parameter imported by its name. Only the parameters' metadata is
// read, so no SecureString value is decrypted here. Any listing error fails
// the service.
func (g *SsmLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	pages := ssm.NewDescribeParametersPaginator(ssm.NewFromConfig(config), &ssm.DescribeParametersInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, parameter := range page.Parameters {
			name := aws.ToString(parameter.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ssm_parameter", "aws"))
		}
	}
	return nil
}

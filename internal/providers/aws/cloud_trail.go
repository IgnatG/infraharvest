// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// CloudTrailLister lists the CloudTrail trails a region reports
// (service cloudtrail).
type CloudTrailLister struct {
	Service
}

// List lists the trails of the region by ARN, which is how
// Terraform imports a trail, named after their names. DescribeTrails
// answers in one call.
//
// A multi-region trail has a shadow copy in every other region, and an
// organization trail one in every member account; both belong to the
// trail in its home region and account, so they are left out: the request
// asks for no shadow trails, and a trail whose home region is another is
// skipped too.
func (g *CloudTrailLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	out, err := cloudtrail.NewFromConfig(config).DescribeTrails(g.Context(), &cloudtrail.DescribeTrailsInput{
		IncludeShadowTrails: aws.Bool(false),
	})
	if err != nil {
		return err
	}
	resources := make([]harvest.Resource, 0, len(out.TrailList))
	for _, trail := range out.TrailList {
		if home := aws.ToString(trail.HomeRegion); home != "" && home != config.Region {
			continue
		}
		resources = append(resources, harvest.RecordBare(
			aws.ToString(trail.TrailARN), aws.ToString(trail.Name), "aws_cloudtrail", "aws"))
	}
	g.Resources = resources
	return nil
}

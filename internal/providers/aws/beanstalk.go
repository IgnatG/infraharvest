// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// BeanstalkLister lists the region's Elastic Beanstalk applications and
// environments.
type BeanstalkLister struct {
	Service
}

// List lists the applications, then the environments. Either
// call failing fails the service.
func (g *BeanstalkLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *BeanstalkLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := elasticbeanstalk.NewFromConfig(config)
	if err := g.listApplications(g.Context(), client); err != nil {
		return err
	}
	return g.listEnvironments(g.Context(), client)
}

// listApplications adds each application as an
// aws_elastic_beanstalk_application, imported by its name. The API returns
// every application in one answer.
func (g *BeanstalkLister) listApplications(ctx context.Context, client *elasticbeanstalk.Client) error {
	out, err := client.DescribeApplications(ctx, &elasticbeanstalk.DescribeApplicationsInput{})
	if err != nil {
		return err
	}
	for _, app := range out.Applications {
		name := aws.ToString(app.ApplicationName)
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_elastic_beanstalk_application", "aws"))
	}
	return nil
}

// listEnvironments adds each environment as an
// aws_elastic_beanstalk_environment, imported by its ID and named after its
// name, across pages. Terminated environments, which the API reports for
// an hour unless asked not to, and those terminating are left out:
// Terraform reads them as gone. The SDK has no paginator for it.
func (g *BeanstalkLister) listEnvironments(ctx context.Context, client *elasticbeanstalk.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.DescribeEnvironments(ctx, &elasticbeanstalk.DescribeEnvironmentsInput{
			IncludeDeleted: aws.Bool(false),
			NextToken:      token,
		})
		if err != nil {
			return nil, err
		}
		for _, env := range out.Environments {
			switch env.Status {
			case types.EnvironmentStatusTerminating, types.EnvironmentStatusTerminated:
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(env.EnvironmentId), aws.ToString(env.EnvironmentName), "aws_elastic_beanstalk_environment", "aws"))
		}
		return out.NextToken, nil
	})
}

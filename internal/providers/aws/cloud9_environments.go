// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloud9"
	"github.com/aws/aws-sdk-go-v2/service/cloud9/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// cloud9DescribeBatch is the most environments DescribeEnvironments
// describes in one call.
const cloud9DescribeBatch = 25

// Cloud9Lister lists a region's Cloud9 EC2 environments by ID, which is
// what Terraform imports them with. Cloud9 is closed to new customers, but
// existing environments go on working and can still be imported.
type Cloud9Lister struct {
	Service
}

// List lists the environments page by page. As each page arrives
// it describes the page's environments, at most 25 to a call, and asks the
// status of each EC2 environment, leaving out those that can't be imported.
// Only EC2 environments are listed, as aws_cloud9_environment_ec2: an SSH
// environment has no Terraform type, so it is logged and skipped, and so is
// one the description leaves out, which was deleted in between. An error
// from any call is returned at once.
func (g *Cloud9Lister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := cloud9.NewFromConfig(config)
	p := cloud9.NewListEnvironmentsPaginator(svc, &cloud9.ListEnvironmentsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		kinds, err := g.environmentTypes(svc, page.EnvironmentIds)
		if err != nil {
			return err
		}
		for _, id := range page.EnvironmentIds {
			kind, described := kinds[id]
			if !described {
				log.Printf("aws: cloud9: skipping environment %s, which was not described", id)
				continue
			}
			if kind != types.EnvironmentTypeEc2 {
				log.Printf("aws: cloud9: skipping environment %s of type %q, which Terraform has no resource for", id, kind)
				continue
			}
			importable, err := g.importable(svc, id)
			if err != nil {
				return err
			}
			if importable {
				g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_cloud9_environment_ec2", "aws"))
			}
		}
	}
	return nil
}

// environmentTypes describes the environments with these IDs, in batches
// of cloud9DescribeBatch, and returns the type of each one described.
func (g *Cloud9Lister) environmentTypes(svc *cloud9.Client, ids []string) (map[string]types.EnvironmentType, error) {
	kinds := make(map[string]types.EnvironmentType, len(ids))
	for batch := range slices.Chunk(ids, cloud9DescribeBatch) {
		out, err := svc.DescribeEnvironments(g.Context(), &cloud9.DescribeEnvironmentsInput{EnvironmentIds: batch})
		if err != nil {
			return nil, err
		}
		for _, env := range out.Environments {
			kinds[aws.ToString(env.Id)] = env.Type
		}
	}
	return kinds, nil
}

// importable reports whether the environment with this ID can be imported:
// any status but error, which never became a working environment, and
// deleting, which will be gone before Terraform could import it. An answer
// without a status counts as importable.
func (g *Cloud9Lister) importable(svc *cloud9.Client, id string) (bool, error) {
	out, err := svc.DescribeEnvironmentStatus(g.Context(), &cloud9.DescribeEnvironmentStatusInput{EnvironmentId: aws.String(id)})
	if err != nil {
		return false, err
	}
	return out.Status != types.EnvironmentStatusError && out.Status != types.EnvironmentStatusDeleting, nil
}

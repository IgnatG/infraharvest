// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SfnLister lists the region's Step Functions state machines and
// activities.
type SfnLister struct {
	Service
}

// List records the state machines, then the activities, each by
// its ARN, which is what Terraform imports both by, and named after it.
// The first error stops the listing and is returned.
func (g *SfnLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *SfnLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := sfn.NewFromConfig(config)

	for page, err := range pagesOf(g.Context(), sfn.NewListStateMachinesPaginator(client, &sfn.ListStateMachinesInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, machine := range page.StateMachines {
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(machine.StateMachineArn),
				aws.ToString(machine.Name),
				"aws_sfn_state_machine",
				"aws"))
		}
	}

	for page, err := range pagesOf(g.Context(), sfn.NewListActivitiesPaginator(client, &sfn.ListActivitiesInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, activity := range page.Activities {
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(activity.ActivityArn),
				aws.ToString(activity.Name),
				"aws_sfn_activity",
				"aws"))
		}
	}
	return nil
}

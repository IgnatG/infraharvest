// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// SfnLister lists the region's Step Functions state machines and
// activities.
type SfnLister struct {
	Service
}

// List records the state machines, then the activities, each by
// its ARN, which is what Terraform imports both by, and named after it.
// The first error stops the listing and is returned.
func (g *SfnLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := sfn.NewFromConfig(config)

	machines := sfn.NewListStateMachinesPaginator(client, &sfn.ListStateMachinesInput{}, stopOnDuplicateToken)
	for machines.HasMorePages() {
		page, err := machines.NextPage(g.Context())
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

	activities := sfn.NewListActivitiesPaginator(client, &sfn.ListActivitiesInput{}, stopOnDuplicateToken)
	for activities.HasMorePages() {
		page, err := activities.NextPage(g.Context())
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

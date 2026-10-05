package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
)

type SfnGenerator struct {
	AWSService
}

func (g *SfnGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := sfn.NewFromConfig(config)

	p := sfn.NewListStateMachinesPaginator(svc, &sfn.ListStateMachinesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, stateMachine := range page.StateMachines {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*stateMachine.StateMachineArn,
				*stateMachine.Name,
				"aws_sfn_state_machine",
				"aws"))
		}
	}

	pActivity := sfn.NewListActivitiesPaginator(svc, &sfn.ListActivitiesInput{}, stopOnDuplicateToken)
	for pActivity.HasMorePages() {
		pActivityNextPage, err := pActivity.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, stateMachine := range pActivityNextPage.Activities {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*stateMachine.ActivityArn,
				*stateMachine.Name,
				"aws_sfn_activity",
				"aws"))
		}
	}

	return nil
}

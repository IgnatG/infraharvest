package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/appsync"
)

type AppSyncGenerator struct {
	AWSService
}

func (g *AppSyncGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}

	svc := appsync.NewFromConfig(config)

	return paginateByMarker(func(nextToken *string) (*string, error) {
		apis, err := svc.ListGraphqlApis(g.Context(), &appsync.ListGraphqlApisInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}

		for _, api := range apis.GraphqlApis {
			var id = *api.ApiId
			var name = *api.Name
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				id,
				name,
				"aws_appsync_graphql_api",
				"aws",
				[]string{}))
		}
		return apis.NextToken, nil
	})
}

package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentity"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
)

type CognitoGenerator struct {
	AWSService
}

const CognitoMaxResults = 60 // Required field for Cognito API

func (g *CognitoGenerator) loadIdentityPools(svc *cognitoidentity.Client) error {
	p := cognitoidentity.NewListIdentityPoolsPaginator(svc, &cognitoidentity.ListIdentityPoolsInput{
		MaxResults: aws.Int32(CognitoMaxResults),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, pool := range page.IdentityPools {
			var id = *pool.IdentityPoolId
			var resourceName = *pool.IdentityPoolName
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				id,
				resourceName+"_"+id,
				"aws_cognito_identity_pool",
				"aws"))
		}
	}

	return nil
}

func (g *CognitoGenerator) loadUserPools(svc *cognitoidentityprovider.Client) ([]string, error) {
	p := cognitoidentityprovider.NewListUserPoolsPaginator(svc, &cognitoidentityprovider.ListUserPoolsInput{
		MaxResults: aws.Int32(CognitoMaxResults),
	}, stopOnDuplicateToken)

	var userPoolIDs []string
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return nil, err
		}
		for _, pool := range page.UserPools {
			id := *pool.Id
			resourceName := *pool.Name
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				id,
				resourceName+"_"+id,
				"aws_cognito_user_pool",
				"aws"))

			userPoolIDs = append(userPoolIDs, *pool.Id)
		}
	}
	return userPoolIDs, nil
}

func (g *CognitoGenerator) loadUserPoolClients(svc *cognitoidentityprovider.Client, userPoolIDs []string) error {
	for _, userPoolID := range userPoolIDs {
		p := cognitoidentityprovider.NewListUserPoolClientsPaginator(svc, &cognitoidentityprovider.ListUserPoolClientsInput{
			UserPoolId: aws.String(userPoolID),
			MaxResults: aws.Int32(CognitoMaxResults),
		}, stopOnDuplicateToken)

		for p.HasMorePages() {
			page, err := p.NextPage(g.Context())
			if err != nil {
				return err
			}
			for _, poolClient := range page.UserPoolClients {
				id := *poolClient.ClientId
				resourceName := *poolClient.ClientName
				g.Resources = append(g.Resources, terraformutils.NewResource(
					id,
					resourceName+"_"+id,
					"aws_cognito_user_pool_client",
					"aws",
					map[string]string{
						"user_pool_id": *poolClient.UserPoolId,
					}))
			}
		}
	}
	return nil
}

func (g *CognitoGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}

	svcCognitoIdentity := cognitoidentity.NewFromConfig(config)
	if err := g.loadIdentityPools(svcCognitoIdentity); err != nil {
		return err
	}
	svcCognitoIdentityProvider := cognitoidentityprovider.NewFromConfig(config)

	userPoolIDs, err := g.loadUserPools(svcCognitoIdentityProvider)
	if err != nil {
		return err
	}
	if err = g.loadUserPoolClients(svcCognitoIdentityProvider, userPoolIDs); err != nil {
		return err
	}

	return nil
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentity"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// cognitoPageSize is the page size of the three Cognito lists. They
// require MaxResults, and 60 is the largest every one of them accepts.
const cognitoPageSize = 60

// CognitoLister lists the region's Cognito identity pools, user pools
// and user pool clients.
type CognitoLister struct {
	Service
}

// List records the identity pools, then the user pools, then the
// clients of each user pool in the order the pools were listed. The first
// error stops the listing and is returned.
func (g *CognitoLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	if err := g.listCognitoIdentityPools(cognitoidentity.NewFromConfig(config)); err != nil {
		return err
	}
	idp := cognitoidentityprovider.NewFromConfig(config)
	poolIDs, err := g.listCognitoUserPools(idp)
	if err != nil {
		return err
	}
	for _, poolID := range poolIDs {
		if err := g.listCognitoUserPoolClients(idp, poolID); err != nil {
			return err
		}
	}
	return nil
}

// listCognitoIdentityPools records each identity pool by ID, named
// <name>_<ID> because pool names need not be unique.
func (g *CognitoLister) listCognitoIdentityPools(client *cognitoidentity.Client) error {
	input := &cognitoidentity.ListIdentityPoolsInput{MaxResults: aws.Int32(cognitoPageSize)}
	pages := cognitoidentity.NewListIdentityPoolsPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, pool := range page.IdentityPools {
			id := aws.ToString(pool.IdentityPoolId)
			g.Resources = append(g.Resources, harvest.RecordBare(
				id,
				aws.ToString(pool.IdentityPoolName)+"_"+id,
				"aws_cognito_identity_pool",
				"aws"))
		}
	}
	return nil
}

// listCognitoUserPools records each user pool by ID, named <name>_<ID>,
// and returns the IDs in listing order so that their clients can be
// listed next.
func (g *CognitoLister) listCognitoUserPools(client *cognitoidentityprovider.Client) ([]string, error) {
	var poolIDs []string
	input := &cognitoidentityprovider.ListUserPoolsInput{MaxResults: aws.Int32(cognitoPageSize)}
	pages := cognitoidentityprovider.NewListUserPoolsPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return nil, err
		}
		for _, pool := range page.UserPools {
			id := aws.ToString(pool.Id)
			poolIDs = append(poolIDs, id)
			g.Resources = append(g.Resources, harvest.RecordBare(
				id,
				aws.ToString(pool.Name)+"_"+id,
				"aws_cognito_user_pool",
				"aws"))
		}
	}
	return poolIDs, nil
}

// listCognitoUserPoolClients records each client of one user pool by its
// client ID, named <name>_<ID>, with its pool's ID as user_pool_id: the
// catalogue builds Terraform's import ID, <user_pool_id>/<client_id>, from
// the two.
func (g *CognitoLister) listCognitoUserPoolClients(client *cognitoidentityprovider.Client, poolID string) error {
	input := &cognitoidentityprovider.ListUserPoolClientsInput{
		UserPoolId: aws.String(poolID),
		MaxResults: aws.Int32(cognitoPageSize),
	}
	pages := cognitoidentityprovider.NewListUserPoolClientsPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, poolClient := range page.UserPoolClients {
			id := aws.ToString(poolClient.ClientId)
			g.Resources = append(g.Resources, harvest.Record(
				id,
				aws.ToString(poolClient.ClientName)+"_"+id,
				"aws_cognito_user_pool_client",
				"aws",
				map[string]string{"user_pool_id": aws.ToString(poolClient.UserPoolId)}))
		}
	}
	return nil
}

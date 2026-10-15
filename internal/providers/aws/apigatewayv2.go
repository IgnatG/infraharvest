// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// APIGatewayV2Lister lists API Gateway's HTTP and WebSocket APIs
// (version 2) with their stages, models, integrations, routes, route
// responses and authorizers, and then the account's VPC links.
//
// Deployments are deliberately not listed.
//
// The stages, integrations and routes that API Gateway made itself, as it
// does for a quick-create API, are marked ApiGatewayManaged and left out:
// they come and go with the API.
type APIGatewayV2Lister struct {
	Service
}

// List lists every API first, then each API's parts, then the VPC
// links. Any error stops the listing and is returned.
func (g *APIGatewayV2Lister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := apigatewayv2.NewFromConfig(config)

	apiIDs, err := g.listAPIs(svc)
	if err != nil {
		return err
	}
	for _, apiID := range apiIDs {
		for _, list := range []func(*apigatewayv2.Client, string) error{
			g.listStages,
			g.listModels,
			g.listIntegrations,
			g.listRoutes,
			g.listAuthorizers,
		} {
			if err := list(svc, apiID); err != nil {
				return err
			}
		}
	}
	return g.listVPCLinks(svc)
}

// listAPIs lists the APIs and returns their IDs, in the order listed. The
// SDK has no paginators for API Gateway version 2, so this and every other
// list follows NextToken with paginateByMarker.
func (g *APIGatewayV2Lister) listAPIs(svc *apigatewayv2.Client) ([]string, error) {
	var apiIDs []string
	err := paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetApis(g.Context(), &apigatewayv2.GetApisInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, api := range output.Items {
			apiID := aws.ToString(api.ApiId)
			apiIDs = append(apiIDs, apiID)
			g.Resources = append(g.Resources, harvest.RecordBare(apiID, aws.ToString(api.Name), "aws_apigatewayv2_api", "aws"))
		}
		return output.NextToken, nil
	})
	return apiIDs, err
}

// listStages lists the API's stages. Terraform imports a stage as
// <API ID>/<stage name>, which also names it.
func (g *APIGatewayV2Lister) listStages(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetStages(g.Context(), &apigatewayv2.GetStagesInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, stage := range output.Items {
			if aws.ToBool(stage.ApiGatewayManaged) {
				continue
			}
			stageName := aws.ToString(stage.StageName)
			id := apiID + "/" + stageName
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_apigatewayv2_stage", "aws", map[string]string{
				"api_id": apiID,
				"name":   stageName,
			}))
		}
		return output.NextToken, nil
	})
}

// listModels lists the API's models, named after their IDs.
func (g *APIGatewayV2Lister) listModels(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetModels(g.Context(), &apigatewayv2.GetModelsInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, model := range output.Items {
			modelID := aws.ToString(model.ModelId)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+modelID, modelID, "aws_apigatewayv2_model", "aws", map[string]string{
				"name":         aws.ToString(model.Name),
				"content_type": aws.ToString(model.ContentType),
				"schema":       aws.ToString(model.Schema),
				"api_id":       apiID,
			}))
		}
		return output.NextToken, nil
	})
}

// listIntegrations lists the API's integrations, named after their IDs.
func (g *APIGatewayV2Lister) listIntegrations(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetIntegrations(g.Context(), &apigatewayv2.GetIntegrationsInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, integration := range output.Items {
			if aws.ToBool(integration.ApiGatewayManaged) {
				continue
			}
			integrationID := aws.ToString(integration.IntegrationId)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+integrationID, integrationID, "aws_apigatewayv2_integration", "aws", map[string]string{
				"api_id":           apiID,
				"integration_type": string(integration.IntegrationType),
			}))
		}
		return output.NextToken, nil
	})
}

// listRoutes lists the API's routes, named after their IDs, and once every
// page of routes is in, each listed route's responses.
func (g *APIGatewayV2Lister) listRoutes(svc *apigatewayv2.Client, apiID string) error {
	var routeIDs []string
	err := paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetRoutes(g.Context(), &apigatewayv2.GetRoutesInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, route := range output.Items {
			if aws.ToBool(route.ApiGatewayManaged) {
				continue
			}
			routeID := aws.ToString(route.RouteId)
			routeIDs = append(routeIDs, routeID)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+routeID, routeID, "aws_apigatewayv2_route", "aws", map[string]string{
				"api_id":    apiID,
				"route_key": aws.ToString(route.RouteKey),
			}))
		}
		return output.NextToken, nil
	})
	if err != nil {
		return err
	}
	for _, routeID := range routeIDs {
		if err := g.listRouteResponses(svc, apiID, routeID); err != nil {
			return err
		}
	}
	return nil
}

// listRouteResponses lists the responses of one route. Terraform imports
// them as <API ID>/<route ID>/<route response ID>; each is named after its
// own ID.
func (g *APIGatewayV2Lister) listRouteResponses(svc *apigatewayv2.Client, apiID, routeID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetRouteResponses(g.Context(), &apigatewayv2.GetRouteResponsesInput{
			ApiId:     aws.String(apiID),
			RouteId:   aws.String(routeID),
			NextToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, response := range output.Items {
			responseID := aws.ToString(response.RouteResponseId)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+routeID+"/"+responseID, responseID, "aws_apigatewayv2_route_response", "aws", map[string]string{
				"api_id":             apiID,
				"route_id":           routeID,
				"route_response_key": aws.ToString(response.RouteResponseKey),
			}))
		}
		return output.NextToken, nil
	})
}

// listAuthorizers lists the API's authorizers, named after their IDs.
func (g *APIGatewayV2Lister) listAuthorizers(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetAuthorizers(g.Context(), &apigatewayv2.GetAuthorizersInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, authorizer := range output.Items {
			authorizerID := aws.ToString(authorizer.AuthorizerId)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+authorizerID, authorizerID, "aws_apigatewayv2_authorizer", "aws", map[string]string{
				"api_id":          apiID,
				"name":            aws.ToString(authorizer.Name),
				"authorizer_type": string(authorizer.AuthorizerType),
			}))
		}
		return output.NextToken, nil
	})
}

// listVPCLinks lists the VPC links of API Gateway version 2, which belong
// to the account rather than to an API. Each is named after its ID.
func (g *APIGatewayV2Lister) listVPCLinks(svc *apigatewayv2.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetVpcLinks(g.Context(), &apigatewayv2.GetVpcLinksInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, link := range output.Items {
			linkID := aws.ToString(link.VpcLinkId)
			g.Resources = append(g.Resources, harvest.RecordBare(linkID, linkID, "aws_apigatewayv2_vpc_link", "aws"))
		}
		return output.NextToken, nil
	})
}

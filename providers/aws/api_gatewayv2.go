// Copyright 2020 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
)

// APIGatewayV2Generator lists API Gateway v2 (HTTP and WebSocket) APIs, with
// their stages, models, routes, route responses and authorizers, and VPC
// links. The v2 API has no SDK paginators, so it pages by NextToken. Each
// resource records the ID Terraform imports it with.
type APIGatewayV2Generator struct {
	AWSService
}

func (g *APIGatewayV2Generator) InitResources() error {
	config, err := g.generateConfig()
	if err != nil {
		return err
	}
	svc := apigatewayv2.NewFromConfig(config)

	if err := g.loadAPIs(svc); err != nil {
		return err
	}
	return g.loadVpcLinks(svc)
}

func (g *APIGatewayV2Generator) loadAPIs(svc *apigatewayv2.Client) error {
	var apiIDs []string
	err := paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetApis(g.Context(), &apigatewayv2.GetApisInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, api := range page.Items {
			apiID := StringValue(api.ApiId)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				apiID,
				apiID+"_"+StringValue(api.Name),
				"aws_apigatewayv2_api",
				"aws"))
			apiIDs = append(apiIDs, apiID)
		}
		return page.NextToken, nil
	})
	if err != nil {
		return err
	}
	for _, apiID := range apiIDs {
		for _, load := range []func(*apigatewayv2.Client, string) error{g.loadStages, g.loadModels, g.loadRoutes, g.loadAuthorizers} {
			if err := load(svc, apiID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *APIGatewayV2Generator) loadStages(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetStages(g.Context(), &apigatewayv2.GetStagesInput{ApiId: &apiID, NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, stage := range page.Items {
			stageID := apiID + "/" + StringValue(stage.StageName)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				stageID,
				stageID,
				"aws_apigatewayv2_stage",
				"aws",
				map[string]string{
					"api_id": apiID,
					"name":   StringValue(stage.StageName),
				}))
		}
		return page.NextToken, nil
	})
}

func (g *APIGatewayV2Generator) loadModels(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetModels(g.Context(), &apigatewayv2.GetModelsInput{ApiId: &apiID, NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, model := range page.Items {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				apiID+"/"+StringValue(model.ModelId),
				StringValue(model.ModelId),
				"aws_apigatewayv2_model",
				"aws",
				map[string]string{
					"name":         StringValue(model.Name),
					"content_type": StringValue(model.ContentType),
					"schema":       StringValue(model.Schema),
					"api_id":       apiID,
				}))
		}
		return page.NextToken, nil
	})
}

func (g *APIGatewayV2Generator) loadRoutes(svc *apigatewayv2.Client, apiID string) error {
	var routeIDs []string
	err := paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetRoutes(g.Context(), &apigatewayv2.GetRoutesInput{ApiId: &apiID, NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, route := range page.Items {
			routeID := StringValue(route.RouteId)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				apiID+"/"+routeID,
				routeID,
				"aws_apigatewayv2_route",
				"aws",
				map[string]string{
					"api_id":    apiID,
					"route_key": StringValue(route.RouteKey),
				}))
			routeIDs = append(routeIDs, routeID)
		}
		return page.NextToken, nil
	})
	if err != nil {
		return err
	}
	for _, routeID := range routeIDs {
		if err := g.loadRouteResponses(svc, apiID, routeID); err != nil {
			return err
		}
	}
	return nil
}

func (g *APIGatewayV2Generator) loadRouteResponses(svc *apigatewayv2.Client, apiID, routeID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetRouteResponses(g.Context(), &apigatewayv2.GetRouteResponsesInput{ApiId: &apiID, RouteId: &routeID, NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, response := range page.Items {
			responseID := StringValue(response.RouteResponseId)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				apiID+"/"+routeID+"/"+responseID,
				responseID,
				"aws_apigatewayv2_route_response",
				"aws",
				map[string]string{
					"api_id":             apiID,
					"route_id":           routeID,
					"route_response_key": StringValue(response.RouteResponseKey),
				}))
		}
		return page.NextToken, nil
	})
}

func (g *APIGatewayV2Generator) loadAuthorizers(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetAuthorizers(g.Context(), &apigatewayv2.GetAuthorizersInput{ApiId: &apiID, NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, authorizer := range page.Items {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				apiID+"/"+StringValue(authorizer.AuthorizerId),
				StringValue(authorizer.AuthorizerId),
				"aws_apigatewayv2_authorizer",
				"aws",
				map[string]string{
					"api_id":          apiID,
					"name":            StringValue(authorizer.Name),
					"authorizer_type": string(authorizer.AuthorizerType),
				}))
		}
		return page.NextToken, nil
	})
}

func (g *APIGatewayV2Generator) loadVpcLinks(svc *apigatewayv2.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		page, err := svc.GetVpcLinks(g.Context(), &apigatewayv2.GetVpcLinksInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, link := range page.Items {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				StringValue(link.VpcLinkId),
				StringValue(link.VpcLinkId),
				"aws_apigatewayv2_vpc_link",
				"aws"))
		}
		return page.NextToken, nil
	})
}

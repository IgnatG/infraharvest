// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

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
// links. Models and route responses exist only for WebSocket APIs, so an
// HTTP API isn't asked for them. Any error stops the listing and is
// returned.
func (g *APIGatewayV2Lister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *APIGatewayV2Lister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := apigatewayv2.NewFromConfig(config)

	apis, err := g.listAPIs(svc)
	if err != nil {
		return err
	}
	for _, api := range apis {
		// Each part takes a call or more per API, so those of types the
		// import doesn't keep are skipped.
		responses := !api.http && g.Wants("aws_apigatewayv2_route_response")
		for _, part := range []struct {
			list   func(*apigatewayv2.Client, string) error
			wanted bool
		}{
			{g.listStages, g.Wants("aws_apigatewayv2_stage")},
			{g.listModels, !api.http && g.Wants("aws_apigatewayv2_model")},
			{g.listIntegrations, g.Wants("aws_apigatewayv2_integration")},
			{func(svc *apigatewayv2.Client, apiID string) error {
				return g.listRoutes(svc, apiID, responses)
			}, responses || g.Wants("aws_apigatewayv2_route")},
			{g.listAuthorizers, g.Wants("aws_apigatewayv2_authorizer")},
		} {
			if !part.wanted {
				continue
			}
			if err := part.list(svc, api.id); err != nil {
				return err
			}
		}
	}
	return g.listVPCLinks(svc)
}

// apiV2Ref is a listed API: its ID, and whether it is an HTTP API, which
// has no models or route responses, rather than a WebSocket one.
type apiV2Ref struct {
	id   string
	http bool
}

// listAPIs lists the APIs and returns them, in the order listed. The SDK
// has no paginators for API Gateway version 2, so this and every other
// list follows NextToken with paginateByMarker.
func (g *APIGatewayV2Lister) listAPIs(svc *apigatewayv2.Client) ([]apiV2Ref, error) {
	var apis []apiV2Ref
	err := paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetApis(g.Context(), &apigatewayv2.GetApisInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, api := range output.Items {
			apiID := aws.ToString(api.ApiId)
			apis = append(apis, apiV2Ref{id: apiID, http: api.ProtocolType == types.ProtocolTypeHttp})
			g.Resources = append(g.Resources, harvest.RecordBare(apiID, aws.ToString(api.Name), "aws_apigatewayv2_api", "aws"))
		}
		return output.NextToken, nil
	})
	return apis, err
}

// listStages lists the API's stages, named after their names. Terraform
// imports a stage as <API ID>/<stage name>.
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
			g.Resources = append(g.Resources, harvest.Record(id, stageName, "aws_apigatewayv2_stage", "aws", map[string]string{
				"api_id": apiID,
				"name":   stageName,
			}))
		}
		return output.NextToken, nil
	})
}

// listModels lists the API's models, named after their names.
func (g *APIGatewayV2Lister) listModels(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetModels(g.Context(), &apigatewayv2.GetModelsInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, model := range output.Items {
			modelID := aws.ToString(model.ModelId)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+modelID, aws.ToString(model.Name), "aws_apigatewayv2_model", "aws", map[string]string{
				"name":         aws.ToString(model.Name),
				"content_type": aws.ToString(model.ContentType),
				"schema":       aws.ToString(model.Schema),
				"api_id":       apiID,
			}))
		}
		return output.NextToken, nil
	})
}

// listIntegrations lists the API's integrations, named after what they
// call (see integrationName).
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
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+integrationID, integrationName(integration), "aws_apigatewayv2_integration", "aws", map[string]string{
				"api_id":           apiID,
				"integration_type": string(integration.IntegrationType),
			}))
		}
		return output.NextToken, nil
	})
}

// listRoutes lists the API's routes, named after their route keys (GET
// /orders), and once every page of routes is in, with responses set, each
// listed route's responses.
func (g *APIGatewayV2Lister) listRoutes(svc *apigatewayv2.Client, apiID string, responses bool) error {
	var routeIDs, routeKeys []string
	err := paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetRoutes(g.Context(), &apigatewayv2.GetRoutesInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, route := range output.Items {
			if aws.ToBool(route.ApiGatewayManaged) {
				continue
			}
			routeID, routeKey := aws.ToString(route.RouteId), aws.ToString(route.RouteKey)
			routeIDs = append(routeIDs, routeID)
			routeKeys = append(routeKeys, routeKey)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+routeID, routeKey, "aws_apigatewayv2_route", "aws", map[string]string{
				"api_id":    apiID,
				"route_key": routeKey,
			}))
		}
		return output.NextToken, nil
	})
	if err != nil || !responses {
		return err
	}
	for i, routeID := range routeIDs {
		if err := g.listRouteResponses(svc, apiID, routeID, routeKeys[i]); err != nil {
			return err
		}
	}
	return nil
}

// listRouteResponses lists the responses of one route. Terraform imports
// them as <API ID>/<route ID>/<route response ID>; each is named after
// the route's key, routeKey, and its own.
func (g *APIGatewayV2Lister) listRouteResponses(svc *apigatewayv2.Client, apiID, routeID, routeKey string) error {
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
			responseID, responseKey := aws.ToString(response.RouteResponseId), aws.ToString(response.RouteResponseKey)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+routeID+"/"+responseID, routeKey+" "+responseKey, "aws_apigatewayv2_route_response", "aws", map[string]string{
				"api_id":             apiID,
				"route_id":           routeID,
				"route_response_key": responseKey,
			}))
		}
		return output.NextToken, nil
	})
}

// listAuthorizers lists the API's authorizers, named after their names.
func (g *APIGatewayV2Lister) listAuthorizers(svc *apigatewayv2.Client, apiID string) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetAuthorizers(g.Context(), &apigatewayv2.GetAuthorizersInput{ApiId: aws.String(apiID), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, authorizer := range output.Items {
			authorizerID := aws.ToString(authorizer.AuthorizerId)
			g.Resources = append(g.Resources, harvest.Record(apiID+"/"+authorizerID, aws.ToString(authorizer.Name), "aws_apigatewayv2_authorizer", "aws", map[string]string{
				"api_id":          apiID,
				"name":            aws.ToString(authorizer.Name),
				"authorizer_type": string(authorizer.AuthorizerType),
			}))
		}
		return output.NextToken, nil
	})
}

// listVPCLinks lists the VPC links of API Gateway version 2, which belong
// to the account rather than to an API. Each is named after its name.
func (g *APIGatewayV2Lister) listVPCLinks(svc *apigatewayv2.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		output, err := svc.GetVpcLinks(g.Context(), &apigatewayv2.GetVpcLinksInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, link := range output.Items {
			linkID := aws.ToString(link.VpcLinkId)
			g.Resources = append(g.Resources, harvest.RecordBare(linkID, aws.ToString(link.Name), "aws_apigatewayv2_vpc_link", "aws"))
		}
		return output.NextToken, nil
	})
}

// integrationName names an integration after what it calls: the function
// of a Lambda integration, the host of an HTTP one (after the method, if
// it isn't ANY), the load balancer of a private one, the action of an AWS
// service one (SQS-SendMessage), and otherwise its type (MOCK).
func integrationName(integration types.Integration) string {
	integrationType := string(integration.IntegrationType)
	target := integrationTarget(aws.ToString(integration.IntegrationUri))
	if target == "" {
		target = aws.ToString(integration.IntegrationSubtype)
	}
	if target == "" {
		return integrationType
	}
	if method := aws.ToString(integration.IntegrationMethod); strings.HasPrefix(integrationType, "HTTP") && method != "" && method != "ANY" {
		return method + " " + target
	}
	return target
}

// integrationTarget returns what the integration URI uri calls: a Lambda
// function's name, whatever the URI wraps the function's ARN in; a URL's
// host; a listener's load balancer; the last part of another ARN; or "".
func integrationTarget(uri string) string {
	if _, function, ok := strings.Cut(uri, ":function:"); ok {
		function, _, _ = strings.Cut(function, "/")
		function, _, _ = strings.Cut(function, ":")
		return function
	}
	if u, err := url.Parse(uri); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		return u.Host
	}
	if strings.HasPrefix(uri, "arn:") {
		if _, listener, ok := strings.Cut(uri, ":listener/"); ok {
			// listener/<app|net>/<load balancer>/<ID>/<ID>
			if parts := strings.Split(listener, "/"); len(parts) > 1 {
				return parts[1]
			}
		}
		return arnName(uri)
	}
	return ""
}

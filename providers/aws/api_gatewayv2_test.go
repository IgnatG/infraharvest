// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"
)

// The lister pages every list by NextToken, keeps the API ID on later pages,
// and records the IDs Terraform imports each resource type with.
func TestAPIGatewayV2ListsWithImportIDs(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		token := call.Query.Get("nextToken")
		switch call.Path {
		case "/v2/apis":
			if token == "" {
				return `{"items":[{"apiId":"a1","name":"orders"}],"nextToken":"t1"}`
			}
			return `{"items":[{"apiId":"a2","name":"users"}]}`
		case "/v2/apis/a1/stages":
			if token == "" {
				return `{"items":[{"stageName":"$default"}],"nextToken":"s1"}`
			}
			return `{"items":[{"stageName":"prod"}]}`
		case "/v2/apis/a1/models":
			return `{"items":[{"modelId":"m1","name":"Order","contentType":"application/json","schema":"{}"}]}`
		case "/v2/apis/a1/routes":
			return `{"items":[{"routeId":"r1","routeKey":"GET /orders"}]}`
		case "/v2/apis/a1/routes/r1/routeresponses":
			return `{"items":[{"routeResponseId":"rr1","routeResponseKey":"$default"}]}`
		case "/v2/apis/a1/authorizers":
			return `{"items":[{"authorizerId":"au1","name":"jwt","authorizerType":"JWT"}]}`
		case "/v2/apis/a2/stages", "/v2/apis/a2/models", "/v2/apis/a2/routes", "/v2/apis/a2/authorizers":
			return `{"items":[]}`
		case "/v2/vpclinks":
			return `{"items":[{"vpcLinkId":"v1","name":"link"}]}`
		}
		t.Errorf("unexpected call %s?%s", call.Path, call.Query.Encode())
		return `{"items":[]}`
	})
	g := &APIGatewayV2Generator{}

	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_apigatewayv2_api", "a1", "a2")
	assertIDs(t, g.Resources, "aws_apigatewayv2_stage", "a1/$default", "a1/prod")
	assertIDs(t, g.Resources, "aws_apigatewayv2_model", "a1/m1")
	assertIDs(t, g.Resources, "aws_apigatewayv2_route", "a1/r1")
	assertIDs(t, g.Resources, "aws_apigatewayv2_route_response", "a1/r1/rr1")
	assertIDs(t, g.Resources, "aws_apigatewayv2_authorizer", "a1/au1")
	assertIDs(t, g.Resources, "aws_apigatewayv2_vpc_link", "v1")
	assertIDs(t, g.Resources, "aws_api_gateway_stage")
}

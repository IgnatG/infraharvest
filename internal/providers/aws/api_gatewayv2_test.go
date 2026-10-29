// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister pages every list by NextToken, keeps the API ID on later pages,
// records the IDs Terraform imports each resource type with, and leaves out
// the routes and integrations API Gateway manages. An HTTP API isn't asked
// for models or route responses, which only WebSocket APIs have.
func TestAPIGatewayV2ListsWithImportIDs(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		token := call.Query.Get("nextToken")
		switch call.Path {
		case "/v2/apis":
			if token == "" {
				return `{"items":[{"apiId":"a1","name":"orders"}],"nextToken":"t1"}`
			}
			return `{"items":[{"apiId":"a2","name":"users"},{"apiId":"a3","name":"shop","protocolType":"HTTP"}]}`
		case "/v2/apis/a1/stages":
			if token == "" {
				return `{"items":[{"stageName":"$default"}],"nextToken":"s1"}`
			}
			return `{"items":[{"stageName":"prod"}]}`
		case "/v2/apis/a1/models":
			return `{"items":[{"modelId":"m1","name":"Order","contentType":"application/json","schema":"{}"}]}`
		case "/v2/apis/a1/routes":
			return `{"items":[{"routeId":"r1","routeKey":"GET /orders"},{"routeId":"r2","routeKey":"$default","apiGatewayManaged":true}]}`
		case "/v2/apis/a1/integrations":
			return `{"items":[{"integrationId":"i1","integrationType":"AWS_PROXY"},{"integrationId":"i2","apiGatewayManaged":true}]}`
		case "/v2/apis/a1/routes/r1/routeresponses":
			return `{"items":[{"routeResponseId":"rr1","routeResponseKey":"$default"}]}`
		case "/v2/apis/a1/authorizers":
			return `{"items":[{"authorizerId":"au1","name":"jwt","authorizerType":"JWT"}]}`
		case "/v2/apis/a2/stages", "/v2/apis/a2/models", "/v2/apis/a2/integrations", "/v2/apis/a2/routes", "/v2/apis/a2/authorizers":
			return `{"items":[]}`
		case "/v2/apis/a3/routes":
			return `{"items":[{"routeId":"r9","routeKey":"GET /cart"}]}`
		case "/v2/apis/a3/stages", "/v2/apis/a3/integrations", "/v2/apis/a3/authorizers":
			// An HTTP API has no models or route responses, so they
			// aren't asked for.
			return `{"items":[]}`
		case "/v2/vpclinks":
			return `{"items":[{"vpcLinkId":"v1","name":"link"}]}`
		}
		t.Errorf("unexpected call %s?%s", call.Path, call.Query.Encode())
		return `{"items":[]}`
	})
	g := &APIGatewayV2Lister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_apigatewayv2_api", "a1", "a2", "a3")
	assertIDs(t, resources, "aws_apigatewayv2_stage", "a1/$default", "a1/prod")
	assertIDs(t, resources, "aws_apigatewayv2_model", "a1/m1")
	assertIDs(t, resources, "aws_apigatewayv2_integration", "a1/i1")
	assertIDs(t, resources, "aws_apigatewayv2_route", "a1/r1", "a3/r9")
	assertIDs(t, resources, "aws_apigatewayv2_route_response", "a1/r1/rr1")
	assertIDs(t, resources, "aws_apigatewayv2_authorizer", "a1/au1")
	assertIDs(t, resources, "aws_apigatewayv2_vpc_link", "v1")
	assertIDs(t, resources, "aws_api_gateway_stage")
}

// Each part is named after what a person reads it by, not its ID: a stage
// by its name, a route by its route key, an integration by what it calls,
// a route response by its route's key and its own. Before, routes and
// integrations were named after IDs such as okoipb1lhk, and stages after
// <API ID>/<stage>.
func TestAPIGatewayV2NamesParts(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Path {
		case "/v2/apis":
			return `{"items":[{"apiId":"a1","name":"orders"}]}`
		case "/v2/apis/a1/stages":
			return `{"items":[{"stageName":"$default"}]}`
		case "/v2/apis/a1/routes":
			return `{"items":[{"routeId":"r1","routeKey":"GET /hello"}]}`
		case "/v2/apis/a1/routes/r1/routeresponses":
			return `{"items":[{"routeResponseId":"rr1","routeResponseKey":"$default"}]}`
		case "/v2/apis/a1/integrations":
			return `{"items":[
				{"integrationId":"i1","integrationType":"AWS_PROXY","integrationMethod":"POST","integrationUri":"arn:aws:lambda:eu-west-2:123456789012:function:shop-fn"},
				{"integrationId":"i2","integrationType":"AWS_PROXY","integrationUri":"arn:aws:apigateway:eu-west-2:lambda:path/2015-03-31/functions/arn:aws:lambda:eu-west-2:123456789012:function:shop-fn:live/invocations"},
				{"integrationId":"i3","integrationType":"HTTP_PROXY","integrationMethod":"GET","integrationUri":"https://api.example.com/v1"},
				{"integrationId":"i4","integrationType":"HTTP_PROXY","integrationMethod":"ANY","connectionType":"VPC_LINK","integrationUri":"arn:aws:elasticloadbalancing:eu-west-2:123456789012:listener/app/shop-web/50dc6c495c0c9188/0467ef3c8400ae65"},
				{"integrationId":"i5","integrationType":"AWS_PROXY","integrationSubtype":"SQS-SendMessage"},
				{"integrationId":"i6","integrationType":"MOCK"}]}`
		case "/v2/apis/a1/models", "/v2/apis/a1/authorizers", "/v2/vpclinks":
			return `{"items":[]}`
		}
		t.Errorf("unexpected call %s?%s", call.Path, call.Query.Encode())
		return `{"items":[]}`
	})
	g := &APIGatewayV2Lister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"a1/$default": "$default",
		"a1/r1":       "GET /hello",
		"a1/r1/rr1":   "GET /hello $default",
		"a1/i1":       "shop-fn",
		"a1/i2":       "shop-fn",
		"a1/i3":       "GET api.example.com",
		"a1/i4":       "shop-web",
		"a1/i5":       "SQS-SendMessage",
		"a1/i6":       "MOCK",
		"a1":          "orders",
	}
	for _, r := range resources {
		if name, ok := want[r.Observed.ID]; !ok || r.RawName != name {
			t.Errorf("%s %s: named %q, want %q", r.Address.Type, r.Observed.ID, r.RawName, name)
		}
	}
	if len(resources) != len(want) {
		t.Errorf("got %d resources, want %d", len(resources), len(want))
	}
}

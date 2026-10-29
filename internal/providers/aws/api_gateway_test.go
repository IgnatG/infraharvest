// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister records the IDs Terraform imports each part of a REST API
// with, lists the deployment a stage serves and not the API's root
// resource, and names each part after the API. A method's integration and
// responses come from GetResources, which embeds them; only a method
// embedded empty is read with GetMethod.
func TestAPIGatewayListsWithImportIDs(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Path {
		case "/restapis":
			return `{"item":[{"id":"a1","name":"orders"}]}`
		case "/restapis/a1/stages":
			return `{"item":[{"stageName":"prod","deploymentId":"d1"},{"stageName":"test","deploymentId":"d1"}]}`
		case "/restapis/a1/resources":
			if call.Query.Get("embed") != "methods" {
				t.Errorf("resources listed without their methods: %s", call.Query.Encode())
			}
			// The methods of r1 are embedded whole, as AWS embeds them, so
			// they take no GetMethod; the root's is embedded empty, as some
			// emulators answer, and is read.
			return `{"item":[
				{"id":"root","path":"/","resourceMethods":{"GET":{}}},
				{"id":"r1","parentId":"root","path":"/health","pathPart":"health","resourceMethods":{
					"POST":{"httpMethod":"POST"},
					"GET":{"httpMethod":"GET","methodIntegration":{"type":"MOCK","integrationResponses":{"200":{"statusCode":"200"}}},
						"methodResponses":{"500":{"statusCode":"500"},"200":{"statusCode":"200"}}}}}]}`
		case "/restapis/a1/resources/root/methods/GET":
			return `{"httpMethod":"GET","methodResponses":{"200":{"statusCode":"200"}}}`
		case "/restapis/a1/models":
			return `{"item":[{"id":"m1","name":"Order","contentType":"application/json"}]}`
		case "/restapis/a1/gatewayresponses":
			return `{"item":[{"responseType":"DEFAULT_4XX","defaultResponse":true},{"responseType":"UNAUTHORIZED","defaultResponse":false}]}`
		case "/restapis/a1/documentation/parts":
			return `{"item":[{"id":"p1"}]}`
		case "/restapis/a1/authorizers":
			return `{"item":[{"id":"au1","name":"tokens"}]}`
		case "/vpclinks", "/usageplans", "/apikeys":
			return `{"item":[]}`
		}
		t.Errorf("unexpected call %s?%s", call.Path, call.Query.Encode())
		return `{"item":[]}`
	})
	g := &APIGatewayLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_api_gateway_rest_api", "a1")
	assertIDs(t, resources, "aws_api_gateway_stage", "a1/prod", "a1/test")
	assertIDs(t, resources, "aws_api_gateway_deployment", "a1/d1")
	assertIDs(t, resources, "aws_api_gateway_resource", "a1/r1")
	assertIDs(t, resources, "aws_api_gateway_method", "a1/root/GET", "a1/r1/GET", "a1/r1/POST")
	assertIDs(t, resources, "aws_api_gateway_integration", "a1/r1/GET")
	assertIDs(t, resources, "aws_api_gateway_integration_response", "a1/r1/GET/200")
	assertIDs(t, resources, "aws_api_gateway_method_response", "a1/root/GET/200", "a1/r1/GET/200", "a1/r1/GET/500")
	assertIDs(t, resources, "aws_api_gateway_model", "a1/Order")
	assertIDs(t, resources, "aws_api_gateway_gateway_response", "a1/UNAUTHORIZED")
	assertIDs(t, resources, "aws_api_gateway_documentation_part", "a1/p1")
	assertIDs(t, resources, "aws_api_gateway_authorizer", "a1/au1")

	names := map[string]string{}
	for _, r := range resources {
		names[r.Address.Type+" "+r.Observed.ID] = r.RawName
	}
	for key, want := range map[string]string{
		"aws_api_gateway_rest_api a1":                        "orders",
		"aws_api_gateway_stage a1/prod":                      "orders_prod",
		"aws_api_gateway_method a1/r1/GET":                   "orders_/health_GET",
		"aws_api_gateway_integration_response a1/r1/GET/200": "orders_/health_GET_200",
	} {
		if got := names[key]; got != want {
			t.Errorf("%s: named %q, want %q", key, got, want)
		}
	}
}

// AWS requires a name for a REST API and its models and authorizers, and
// for VPC links, usage plans and API keys, but an emulator may answer
// without one: each is then named after its ID, and a model, which
// Terraform imports by its name, is left out. Before, the missing name was
// dereferenced, and the panic ended the whole run.
func TestAPIGatewayWithoutNames(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Path {
		case "/restapis":
			return `{"item":[{"id":"a1"}]}`
		case "/restapis/a1/models":
			return `{"item":[{"id":"m1","contentType":"application/json"}]}`
		case "/restapis/a1/authorizers":
			return `{"item":[{"id":"au1"}]}`
		case "/vpclinks":
			return `{"item":[{"id":"v1"}]}`
		case "/usageplans":
			return `{"item":[{"id":"u1"}]}`
		case "/apikeys":
			return `{"item":[{"id":"k1"}]}`
		}
		return `{"item":[]}`
	})
	g := &APIGatewayLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_api_gateway_model")
	names := map[string]string{}
	for _, r := range resources {
		names[r.Address.Type+" "+r.Observed.ID] = r.RawName
	}
	for key, want := range map[string]string{
		"aws_api_gateway_rest_api a1":       "a1",
		"aws_api_gateway_authorizer a1/au1": "a1_au1",
		"aws_api_gateway_vpc_link v1":       "v1",
		"aws_api_gateway_usage_plan u1":     "u1",
		"aws_api_gateway_api_key k1":        "k1",
	} {
		if got, ok := names[key]; !ok || got != want {
			t.Errorf("%s: named %q, want %q", key, got, want)
		}
	}
}

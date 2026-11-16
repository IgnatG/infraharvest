// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// APIGatewayLister lists API Gateway's REST APIs (version 1), each
// followed by the parts of it that Terraform manages as resources of their
// own, and then the account's VPC links, usage plans and API keys, which
// belong to no single API.
type APIGatewayLister struct {
	Service
}

// restAPIRef is the REST API whose parts are being listed. Every part's
// import ID starts with the API's ID, and every part's name with the API's
// name.
type restAPIRef struct {
	id, name string
}

// part names a part of the API: the API's name followed by parts, joined
// with underscores, as in orders_/health_GET.
func (api restAPIRef) part(parts ...string) string {
	return strings.Join(append([]string{api.name}, parts...), "_")
}

// List lists the REST APIs with their parts, then the VPC links,
// usage plans and API keys. A part of a REST API that can't be read, such
// as its stages, is left out, the API and its other parts are kept, and
// the failure makes the listing partial (see harvest.Parts); an API
// deleted since it was listed has no parts. Any other error stops the
// listing and is returned.
func (g *APIGatewayLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *APIGatewayLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := apigateway.NewFromConfig(config)

	var parts harvest.Parts
	for _, list := range []func() error{
		func() error { return g.listRestAPIs(svc, &parts) },
		func() error { return g.listVPCLinks(svc) },
		func() error { return g.listUsagePlans(svc) },
		func() error { return g.listAPIKeys(svc) },
	} {
		if err := list(); err != nil {
			return parts.Fail(err)
		}
	}
	return parts.Err()
}

// listRestAPIs lists the REST APIs that pass the tag filter, and after each
// API all of its parts. A part that can't be read goes to parts.
func (g *APIGatewayLister) listRestAPIs(svc *apigateway.Client, parts *harvest.Parts) error {
	for page, err := range pagesOf(g.Context(), apigateway.NewGetRestApisPaginator(svc, &apigateway.GetRestApisInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, restAPI := range page.Items {
			if !g.keepRestAPI(restAPI.Tags) {
				// The API's parts are left out with it.
				continue
			}
			// AWS requires a name for every REST API, but an emulator
			// may leave it out: the API, and its parts, are then named
			// after its ID.
			id := aws.ToString(restAPI.Id)
			api := restAPIRef{id: id, name: cmp.Or(aws.ToString(restAPI.Name), id)}
			g.Resources = append(g.Resources, harvest.RecordBare(api.id, api.name, "aws_api_gateway_rest_api", "aws"))

			// Each part takes a call or more per API, so those of types
			// the import doesn't keep are skipped.
			for _, part := range []struct {
				what  string
				list  func(*apigateway.Client, restAPIRef) error
				types []string
			}{
				{"stages", g.listStages, []string{"aws_api_gateway_stage", "aws_api_gateway_deployment"}},
				{"resources", g.listResources, []string{"aws_api_gateway_resource", "aws_api_gateway_method", "aws_api_gateway_integration", "aws_api_gateway_integration_response", "aws_api_gateway_method_response"}},
				{"models", g.listModels, []string{"aws_api_gateway_model"}},
				{"gateway responses", g.listResponses, []string{"aws_api_gateway_gateway_response"}},
				{"documentation parts", g.listDocumentationParts, []string{"aws_api_gateway_documentation_part"}},
				{"authorizers", g.listAuthorizers, []string{"aws_api_gateway_authorizer"}},
			} {
				if !g.WantsAny(part.types...) {
					continue
				}
				parts.Add(awsCodes, "the "+part.what+" of REST API "+api.id, part.list(svc, api))
			}
		}
	}
	return nil
}

// keepRestAPI reports whether a REST API with these tags passes the tag
// filter, such as --tag team=payments.
// Only the first tag filter that concerns REST APIs (by naming their type or
// no type at all) is consulted. It keeps an API that has the tag with one of
// the filter's values; a filter without values only asks for the tag.
// Without such a filter, every API is kept.
func (g *APIGatewayLister) keepRestAPI(tags map[string]string) bool {
	for _, filter := range g.Filters() {
		key, isTagFilter := strings.CutPrefix(filter.Path, "tags.")
		if !isTagFilter || !filter.AppliesTo("api_gateway_rest_api") {
			continue
		}
		value, tagged := tags[key]
		if !tagged {
			return false
		}
		return filter.Values == nil || slices.Contains(filter.Values, value)
	}
	return true
}

// listStages lists the API's stages and the deployments they serve. A
// deployment no stage serves is left out. A deployment several stages
// serve is listed once, named after the first of them.
func (g *APIGatewayLister) listStages(svc *apigateway.Client, api restAPIRef) error {
	// GetStages returns every stage at once; it does not page.
	output, err := svc.GetStages(g.Context(), &apigateway.GetStagesInput{RestApiId: aws.String(api.id)})
	if err != nil {
		return err
	}
	var deployments []harvest.Resource
	seen := map[string]bool{}
	for _, stage := range output.Item {
		stageName := aws.ToString(stage.StageName)
		g.Resources = append(g.Resources, harvest.Record(
			api.id+"/"+stageName,
			api.part(stageName),
			"aws_api_gateway_stage",
			"aws",
			map[string]string{
				"rest_api_id": api.id,
				"stage_name":  stageName,
			},
		))

		deploymentID := aws.ToString(stage.DeploymentId)
		if deploymentID == "" || seen[deploymentID] {
			continue
		}
		seen[deploymentID] = true
		deployments = append(deployments, harvest.Record(
			api.id+"/"+deploymentID,
			api.part(stageName),
			"aws_api_gateway_deployment",
			"aws",
			map[string]string{"rest_api_id": api.id},
		))
	}
	g.Resources = append(g.Resources, deployments...)
	return nil
}

// listResources lists the API's resources (its paths) with their methods,
// which GetResources embeds so that they need no call per resource. The
// root resource, "/", comes with the API and is not listed, but its methods
// are.
func (g *APIGatewayLister) listResources(svc *apigateway.Client, api restAPIRef) error {
	p := apigateway.NewGetResourcesPaginator(svc, &apigateway.GetResourcesInput{
		RestApiId: aws.String(api.id),
		Embed:     []string{"methods"},
	}, stopOnDuplicateToken)
	for page, err := range pagesOf(g.Context(), p) {
		if err != nil {
			return err
		}
		for _, resource := range page.Items {
			if resource.ParentId != nil {
				resourceID := aws.ToString(resource.Id)
				path := aws.ToString(resource.Path)
				g.Resources = append(g.Resources, harvest.Record(
					api.id+"/"+resourceID,
					api.part(path),
					"aws_api_gateway_resource",
					"aws",
					map[string]string{
						"path":        path,
						"path_part":   aws.ToString(resource.PathPart),
						"parent_id":   aws.ToString(resource.ParentId),
						"rest_api_id": api.id,
					},
				))
			}
			if err := g.listResourceMethods(svc, api, resource); err != nil {
				return err
			}
		}
	}
	return nil
}

// listResourceMethods lists the methods of one resource, in the order of
// their HTTP methods. GetResources embeds each whole method, with its
// integration, the integration's responses and the method's responses,
// which are listed after it in that order, each set ordered by status code.
// Only a method embedded without its HTTP method, empty, is read with
// GetMethod.
func (g *APIGatewayLister) listResourceMethods(svc *apigateway.Client, api restAPIRef, resource types.Resource) error {
	resourceID := aws.ToString(resource.Id)
	path := aws.ToString(resource.Path)
	for _, httpMethod := range slices.Sorted(maps.Keys(resource.ResourceMethods)) {
		methodID := api.id + "/" + resourceID + "/" + httpMethod
		// Every resource of a method has these attributes, and some add
		// to them, so each gets a map of its own.
		attributes := func() map[string]string {
			return map[string]string{
				"rest_api_id": api.id,
				"resource_id": resourceID,
				"http_method": httpMethod,
			}
		}
		g.Resources = append(g.Resources, harvest.Record(methodID, api.part(path, httpMethod), "aws_api_gateway_method", "aws", attributes()))

		method := resource.ResourceMethods[httpMethod]
		if method.HttpMethod == nil {
			// An answer that only names the method, as some emulators
			// give.
			read, err := svc.GetMethod(g.Context(), &apigateway.GetMethodInput{
				RestApiId:  aws.String(api.id),
				ResourceId: aws.String(resourceID),
				HttpMethod: aws.String(httpMethod),
			})
			if err != nil {
				return err
			}
			method = types.Method{HttpMethod: read.HttpMethod, MethodIntegration: read.MethodIntegration, MethodResponses: read.MethodResponses}
		}

		if integration := method.MethodIntegration; integration != nil {
			integrationAttributes := attributes()
			integrationAttributes["type"] = string(integration.Type)
			g.Resources = append(g.Resources, harvest.Record(methodID, api.part(path, httpMethod), "aws_api_gateway_integration", "aws", integrationAttributes))

			for _, statusCode := range slices.Sorted(maps.Keys(integration.IntegrationResponses)) {
				responseAttributes := attributes()
				responseAttributes["status_code"] = statusCode
				g.Resources = append(g.Resources, harvest.Record(
					methodID+"/"+statusCode,
					api.part(path, httpMethod, statusCode),
					"aws_api_gateway_integration_response",
					"aws",
					responseAttributes,
				))
			}
		}

		for _, statusCode := range slices.Sorted(maps.Keys(method.MethodResponses)) {
			responseAttributes := attributes()
			responseAttributes["status_code"] = statusCode
			g.Resources = append(g.Resources, harvest.Record(
				methodID+"/"+statusCode,
				api.part(path, httpMethod, statusCode),
				"aws_api_gateway_method_response",
				"aws",
				responseAttributes,
			))
		}
	}
	return nil
}

// listModels lists the API's models. Terraform imports a model by its
// name. The catalogue leaves out the Empty and Error models AWS creates
// with every REST API.
func (g *APIGatewayLister) listModels(svc *apigateway.Client, api restAPIRef) error {
	for page, err := range pagesOf(g.Context(), apigateway.NewGetModelsPaginator(svc, &apigateway.GetModelsInput{RestApiId: aws.String(api.id)}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, model := range page.Items {
			// AWS requires a name for every model, and Terraform imports
			// it by its name: a model an emulator answers without one
			// can't be imported, and is left out.
			name := aws.ToString(model.Name)
			if name == "" {
				continue
			}
			g.Resources = append(g.Resources, harvest.Record(
				api.id+"/"+name,
				api.part(name),
				"aws_api_gateway_model",
				"aws",
				map[string]string{
					"name":         name,
					"content_type": aws.ToString(model.ContentType),
					"rest_api_id":  api.id,
				},
			))
		}
	}
	return nil
}

// listResponses lists the gateway responses the API customises. Every API
// has a response of each type, so the defaults it has not changed are left
// out. The SDK has no paginator for GetGatewayResponses, so the pages are
// followed by their position until a page returns none.
func (g *APIGatewayLister) listResponses(svc *apigateway.Client, api restAPIRef) error {
	input := &apigateway.GetGatewayResponsesInput{RestApiId: aws.String(api.id)}
	return paginateByMarker(func(position *string) (*string, error) {
		input.Position = position
		output, err := svc.GetGatewayResponses(g.Context(), input)
		if err != nil {
			return nil, err
		}
		for _, response := range output.Items {
			if response.DefaultResponse {
				continue
			}
			responseType := string(response.ResponseType)
			g.Resources = append(g.Resources, harvest.Record(
				api.id+"/"+responseType,
				api.part(responseType),
				"aws_api_gateway_gateway_response",
				"aws",
				map[string]string{
					"rest_api_id":   api.id,
					"response_type": responseType,
				},
			))
		}
		return output.Position, nil
	})
}

// listDocumentationParts lists the API's documentation parts, following
// the pages by their position as listResponses does.
func (g *APIGatewayLister) listDocumentationParts(svc *apigateway.Client, api restAPIRef) error {
	input := &apigateway.GetDocumentationPartsInput{RestApiId: aws.String(api.id)}
	return paginateByMarker(func(position *string) (*string, error) {
		input.Position = position
		output, err := svc.GetDocumentationParts(g.Context(), input)
		if err != nil {
			return nil, err
		}
		for _, part := range output.Items {
			partID := aws.ToString(part.Id)
			g.Resources = append(g.Resources, harvest.Record(
				api.id+"/"+partID,
				api.part(partID),
				"aws_api_gateway_documentation_part",
				"aws",
				map[string]string{"rest_api_id": api.id},
			))
		}
		return output.Position, nil
	})
}

// listAuthorizers lists the API's authorizers, following the pages by
// their position as listResponses does.
func (g *APIGatewayLister) listAuthorizers(svc *apigateway.Client, api restAPIRef) error {
	input := &apigateway.GetAuthorizersInput{RestApiId: aws.String(api.id)}
	return paginateByMarker(func(position *string) (*string, error) {
		input.Position = position
		output, err := svc.GetAuthorizers(g.Context(), input)
		if err != nil {
			return nil, err
		}
		for _, authorizer := range output.Items {
			// AWS requires a name for every authorizer, but an emulator
			// may leave it out: it is then named after its ID.
			authorizerID := aws.ToString(authorizer.Id)
			name := aws.ToString(authorizer.Name)
			g.Resources = append(g.Resources, harvest.Record(
				api.id+"/"+authorizerID,
				api.part(cmp.Or(name, authorizerID)),
				"aws_api_gateway_authorizer",
				"aws",
				map[string]string{
					"rest_api_id": api.id,
					"name":        name,
				},
			))
		}
		return output.Position, nil
	})
}

// listVPCLinks lists the VPC links of API Gateway version 1, which private
// integrations reach Network Load Balancers through.
func (g *APIGatewayLister) listVPCLinks(svc *apigateway.Client) error {
	for page, err := range pagesOf(g.Context(), apigateway.NewGetVpcLinksPaginator(svc, &apigateway.GetVpcLinksInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, link := range page.Items {
			id := aws.ToString(link.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, cmp.Or(aws.ToString(link.Name), id), "aws_api_gateway_vpc_link", "aws"))
		}
	}
	return nil
}

// listUsagePlans lists the usage plans, which set the throttling and quotas
// of API keys on stages.
func (g *APIGatewayLister) listUsagePlans(svc *apigateway.Client) error {
	for page, err := range pagesOf(g.Context(), apigateway.NewGetUsagePlansPaginator(svc, &apigateway.GetUsagePlansInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, plan := range page.Items {
			id := aws.ToString(plan.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, cmp.Or(aws.ToString(plan.Name), id), "aws_api_gateway_usage_plan", "aws"))
		}
	}
	return nil
}

// listAPIKeys lists the API keys. The keys' values are not asked for.
func (g *APIGatewayLister) listAPIKeys(svc *apigateway.Client) error {
	for page, err := range pagesOf(g.Context(), apigateway.NewGetApiKeysPaginator(svc, &apigateway.GetApiKeysInput{}, stopOnDuplicateToken)) {
		if err != nil {
			return err
		}
		for _, key := range page.Items {
			id := aws.ToString(key.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, cmp.Or(aws.ToString(key.Name), id), "aws_api_gateway_api_key", "aws"))
		}
	}
	return nil
}

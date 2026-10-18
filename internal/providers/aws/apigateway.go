// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
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
// usage plans and API keys. Any error stops the listing and is returned.
func (g *APIGatewayLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := apigateway.NewFromConfig(config)

	if err := g.listRestAPIs(svc); err != nil {
		return err
	}
	if err := g.listVPCLinks(svc); err != nil {
		return err
	}
	if err := g.listUsagePlans(svc); err != nil {
		return err
	}
	return g.listAPIKeys(svc)
}

// listRestAPIs lists the REST APIs that pass the tag filter, and after each
// API all of its parts.
func (g *APIGatewayLister) listRestAPIs(svc *apigateway.Client) error {
	p := apigateway.NewGetRestApisPaginator(svc, &apigateway.GetRestApisInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, restAPI := range page.Items {
			if !g.keepRestAPI(restAPI.Tags) {
				// The API's parts are left out with it.
				continue
			}
			// AWS requires a name for every REST API, so Name is
			// dereferenced as it is.
			api := restAPIRef{id: aws.ToString(restAPI.Id), name: *restAPI.Name}
			g.Resources = append(g.Resources, harvest.RecordBare(api.id, api.name, "aws_api_gateway_rest_api", "aws"))

			for _, list := range []func(*apigateway.Client, restAPIRef) error{
				g.listStages,
				g.listResources,
				g.listModels,
				g.listResponses,
				g.listDocumentationParts,
				g.listAuthorizers,
			} {
				if err := list(svc, api); err != nil {
					return err
				}
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
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
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
	p := apigateway.NewGetModelsPaginator(svc, &apigateway.GetModelsInput{RestApiId: aws.String(api.id)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, model := range page.Items {
			// AWS requires a name for every model.
			name := *model.Name
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
	for {
		output, err := svc.GetGatewayResponses(g.Context(), input)
		if err != nil {
			return err
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
		if aws.ToString(output.Position) == "" {
			return nil
		}
		input.Position = output.Position
	}
}

// listDocumentationParts lists the API's documentation parts, following
// the pages by their position as listResponses does.
func (g *APIGatewayLister) listDocumentationParts(svc *apigateway.Client, api restAPIRef) error {
	input := &apigateway.GetDocumentationPartsInput{RestApiId: aws.String(api.id)}
	for {
		output, err := svc.GetDocumentationParts(g.Context(), input)
		if err != nil {
			return err
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
		if aws.ToString(output.Position) == "" {
			return nil
		}
		input.Position = output.Position
	}
}

// listAuthorizers lists the API's authorizers, following the pages by
// their position as listResponses does.
func (g *APIGatewayLister) listAuthorizers(svc *apigateway.Client, api restAPIRef) error {
	input := &apigateway.GetAuthorizersInput{RestApiId: aws.String(api.id)}
	for {
		output, err := svc.GetAuthorizers(g.Context(), input)
		if err != nil {
			return err
		}
		for _, authorizer := range output.Items {
			// AWS requires a name for every authorizer.
			name := *authorizer.Name
			g.Resources = append(g.Resources, harvest.Record(
				api.id+"/"+aws.ToString(authorizer.Id),
				api.part(name),
				"aws_api_gateway_authorizer",
				"aws",
				map[string]string{
					"rest_api_id": api.id,
					"name":        name,
				},
			))
		}
		if aws.ToString(output.Position) == "" {
			return nil
		}
		input.Position = output.Position
	}
}

// listVPCLinks lists the VPC links of API Gateway version 1, which private
// integrations reach Network Load Balancers through.
func (g *APIGatewayLister) listVPCLinks(svc *apigateway.Client) error {
	p := apigateway.NewGetVpcLinksPaginator(svc, &apigateway.GetVpcLinksInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, link := range page.Items {
			g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(link.Id), *link.Name, "aws_api_gateway_vpc_link", "aws"))
		}
	}
	return nil
}

// listUsagePlans lists the usage plans, which set the throttling and quotas
// of API keys on stages.
func (g *APIGatewayLister) listUsagePlans(svc *apigateway.Client) error {
	p := apigateway.NewGetUsagePlansPaginator(svc, &apigateway.GetUsagePlansInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, plan := range page.Items {
			g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(plan.Id), *plan.Name, "aws_api_gateway_usage_plan", "aws"))
		}
	}
	return nil
}

// listAPIKeys lists the API keys. The keys' values are not asked for.
func (g *APIGatewayLister) listAPIKeys(svc *apigateway.Client) error {
	p := apigateway.NewGetApiKeysPaginator(svc, &apigateway.GetApiKeysInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, key := range page.Items {
			g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(key.Id), *key.Name, "aws_api_gateway_api_key", "aws"))
		}
	}
	return nil
}

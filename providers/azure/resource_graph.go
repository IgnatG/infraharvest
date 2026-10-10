// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// resourceGraphService lists through Resource Graph. Opt-in: * lists with
// the other services, which list some of the same resources.
const resourceGraphService = "resource_graph"

// ResourceGraphGenerator lists every resource of the subscription, or of
// --resource-group, that Azure Resource Graph knows, with the resource
// groups they are in and the child resources Resource Graph doesn't index
// (such as subnets and blob containers), read from Resource Manager. aztft
// maps each to its azurerm type and import ID, asking Resource Manager when
// the ID alone can't say, as for a function app and a web app. Resources
// another resource manages (managedBy), such as an AKS cluster's nodes,
// are left out.
type ResourceGraphGenerator struct {
	AzureService
}

// resourceGroupName is what Azure allows in a resource group's name; the
// name goes into a Resource Graph query.
var resourceGroupName = regexp.MustCompile(`^[-\w.()]+$`)

// subscriptionIDPattern is a subscription ID, which goes into the query.
var subscriptionIDPattern = regexp.MustCompile(`^[0-9A-Fa-f-]+$`)

// graphPredicate is the Resource Graph filter for the subscription, or the
// resource group in it.
func graphPredicate(subscriptionID, resourceGroup string) (string, error) {
	if !subscriptionIDPattern.MatchString(subscriptionID) {
		return "", fmt.Errorf("subscription ID %q isn't a GUID", subscriptionID)
	}
	predicate := fmt.Sprintf("subscriptionId =~ '%s'", subscriptionID)
	if resourceGroup != "" {
		if !resourceGroupName.MatchString(resourceGroup) {
			return "", fmt.Errorf("resource group name %q has characters Azure doesn't allow", resourceGroup)
		}
		predicate += fmt.Sprintf(" and resourceGroup =~ '%s'", resourceGroup)
	}
	return predicate, nil
}

func (g *ResourceGraphGenerator) InitResources() error {
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	predicate, err := graphPredicate(subscriptionID, resourceGroup)
	if err != nil {
		return err
	}
	clientOptions := arm.ClientOptions{}
	if options != nil {
		clientOptions = *options
	}
	lister, err := azlist.NewLister(azlist.Option{
		SubscriptionId:       subscriptionID,
		Cred:                 credential,
		ClientOpt:            clientOptions,
		Recursive:            true,
		IncludeResourceGroup: true,
	})
	if err != nil {
		return err
	}
	result, err := lister.ListByQuery(g.Context(), predicate)
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		// Child resource types a resource doesn't have, or the caller
		// can't read: the rest are listed.
		log.Printf("azurerm: resource_graph: %v", e)
	}
	apiOption := &aztft.APIOption{Cred: credential, ClientOption: clientOptions}
	g.Resources = graphResources(result.Resources, func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, apiOption)
	})
	return nil
}

// queryType maps an Azure resource ID to its azurerm types and import IDs,
// and says whether the match is exact (see aztft.QueryTypeAndId).
type queryType func(id string) (types []aztft.Type, ids []string, exact bool, err error)

// graphResources turns what Resource Graph and Resource Manager listed
// into azurerm resources, through query: one per azurerm type a resource
// maps to, such as a storage account and its queue properties. Resources
// azurerm has no type for, or several it can't choose between, are logged
// and left out.
func graphResources(listed []azlist.AzureResource, query queryType) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, res := range listed {
		id := res.Id.String()
		types, ids, exact, err := query(id)
		switch {
		case err != nil:
			log.Printf("azurerm: resource_graph: %s: %v", id, err)
			continue
		case len(types) == 0:
			log.Printf("azurerm: resource_graph: %s: no azurerm resource type; left out", id)
			continue
		case !exact:
			names := make([]string, len(types))
			for i, t := range types {
				names[i] = t.TFType
			}
			log.Printf("azurerm: resource_graph: %s could be %s; left out", id, strings.Join(names, " or "))
			continue
		}
		attributes := tagAttributes(res.Properties["tags"])
		for i, t := range types {
			resources = append(resources, terraformutils.NewResource(ids[i], lastSegment(t.AzureId.String()), t.TFType, "azurerm", attributes))
		}
	}
	return resources
}

// tagAttributes records a resource's tags as Terraform flattens them,
// tags.<key>, for selection rules and reports.
func tagAttributes(tags interface{}) map[string]string {
	attributes := map[string]string{}
	m, ok := tags.(map[string]interface{})
	if !ok {
		return attributes
	}
	for k, value := range m {
		if v, ok := value.(string); ok {
			attributes["tags."+k] = v
		}
	}
	return attributes
}

// lastSegment is the name a resource ID ends with.
func lastSegment(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

// OptInServices are the services * leaves out (see resourceGraphService).
func (p *AzureProvider) OptInServices() []string {
	return []string{resourceGraphService}
}

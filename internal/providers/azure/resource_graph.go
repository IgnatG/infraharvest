// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"
	"github.com/magodo/armid"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// resourceGraphService lists through Resource Graph. It is what * lists,
// with the few services that list what Resource Graph can't (see
// resourceManagerServices).
const resourceGraphService = "resource_graph"

// resourceManagerServices are the services that list, through Resource
// Manager and one kind at a time, resources that resource_graph lists too.
// They are opt-in: * leaves them out, since listing both would import the
// same resources twice. They are for clouds without Resource Graph, such
// as Azure Stack Hub and emulators, where resource_graph fails, and are
// listed only when --services names them.
var resourceManagerServices = []string{
	"managed_identity", "network_interface", "network_security_group", "public_ip",
	"resource_group", "subnet", "virtual_network",
}

// ResourceGraphLister lists every resource of the subscription, or of
// --resource-group, that Azure Resource Graph knows, every resource group
// of that scope, empty ones too, and the child resources Resource Graph
// doesn't index (such as subnets and blob containers), read from Resource
// Manager. The catalogue's inventory entries map a resource to its azurerm
// type, else aztft does, with its import ID, asking Resource Manager when
// the ID alone can't say, as for a function app and a web app. Resources
// another resource manages (managedBy), such as an AKS cluster's nodes,
// are left out; the catalogue's skip rules leave out what Azure creates
// itself, such as NetworkWatcherRG.
type ResourceGraphLister struct {
	Service
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

func (g *ResourceGraphLister) List() error {
	subscriptionID, resourceGroup, credential, options := g.clientSettings()
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
	// azlist adds only the groups that hold a resource it listed.
	groups, err := graphResourceGroups(g.Context(), credential, &clientOptions, subscriptionID, predicate)
	if err != nil {
		return err
	}
	found := withResourceGroups(result.Resources, groups)
	apiOption := &aztft.APIOption{Cred: credential, ClientOption: clientOptions}
	g.Resources = graphResources(found, g.catalogue(), func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, apiOption)
	})
	return nil
}

// resourceGroupsQuery lists the resource groups that predicate (see
// graphPredicate) selects, with what graphAttributes records of them.
func resourceGroupsQuery(predicate string) string {
	return "resourcecontainers | where type =~ 'microsoft.resources/subscriptions/resourcegroups' and " +
		predicate + " | project id, tags, managedBy | order by id asc"
}

// graphResourceGroups returns the resource groups of the subscription, or
// --resource-group alone, from Resource Graph, as azlist lists resources:
// the ID and, as properties, the tags and what manages the group. A row
// whose ID doesn't parse is logged and left out.
func graphResourceGroups(ctx context.Context, credential azcore.TokenCredential, options *arm.ClientOptions, subscriptionID, predicate string) ([]azlist.AzureResource, error) {
	client, err := armresourcegraph.NewClient(credential, options)
	if err != nil {
		return nil, err
	}
	request := armresourcegraph.QueryRequest{
		Query:         to.Ptr(resourceGroupsQuery(predicate)),
		Subscriptions: []*string{to.Ptr(subscriptionID)},
		Options:       &armresourcegraph.QueryRequestOptions{ResultFormat: to.Ptr(armresourcegraph.ResultFormatObjectArray)},
	}
	var groups []azlist.AzureResource
	for {
		resp, err := client.Resources(ctx, request, nil)
		if err != nil {
			return nil, fmt.Errorf("resource groups from Resource Graph: %w", err)
		}
		rows, _ := resp.Data.([]interface{})
		for _, row := range rows {
			properties, _ := row.(map[string]interface{})
			id, _ := properties["id"].(string)
			if id == "" {
				continue
			}
			rid, err := armid.ParseResourceId(id)
			if err != nil {
				log.Printf("azurerm: resource_graph: resource group %s: %v", id, err)
				continue
			}
			groups = append(groups, azlist.AzureResource{Id: rid, Properties: properties})
		}
		if resp.SkipToken == nil || *resp.SkipToken == "" {
			return groups, nil
		}
		request.Options.SkipToken = resp.SkipToken
	}
}

// withResourceGroups adds to listed each of groups that it doesn't hold
// yet. IDs match whatever their case, as in Azure.
func withResourceGroups(listed, groups []azlist.AzureResource) []azlist.AzureResource {
	seen := map[string]bool{}
	for _, res := range listed {
		if _, ok := res.Id.(*armid.ResourceGroup); ok {
			seen[strings.ToUpper(res.Id.String())] = true
		}
	}
	for _, group := range groups {
		key := strings.ToUpper(group.Id.String())
		if !seen[key] {
			seen[key] = true
			listed = append(listed, group)
		}
	}
	return listed
}

// queryType maps an Azure resource ID to its azurerm types and import IDs,
// and says whether the match is exact (see aztft.QueryTypeAndId).
type queryType func(id string) (types []aztft.Type, ids []string, exact bool, err error)

// graphResources turns what Resource Graph and Resource Manager listed
// into azurerm resources: through the catalogue's inventory entries, which
// override aztft, else through query, one per azurerm type a resource maps
// to, such as a storage account and its queue properties. Resources
// azurerm has no type for, or several it can't choose between, are logged
// and left out.
func graphResources(listed []azlist.AzureResource, cat *catalogue.Catalogue, query queryType) []harvest.Resource {
	managedGroups := managedResourceGroups(listed)
	var resources []harvest.Resource
	for _, res := range listed {
		id := res.Id.String()
		attributes := graphAttributes(res, managedGroups)
		switch types := cat.FromInventory(res.Id.TypeString(), id); len(types) {
		case 0:
		case 1:
			resources = append(resources, harvest.Record(id, lastSegment(id), types[0], "azurerm", attributes))
			continue
		default:
			log.Printf("azurerm: resource_graph: %s could be %s, says the catalogue; left out", id, strings.Join(types, " or "))
			continue
		}
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
		for i, t := range types {
			resources = append(resources, harvest.Record(ids[i], lastSegment(t.AzureId.String()), t.TFType, "azurerm", attributes))
		}
	}
	return resources
}

// managedResourceGroups returns the resource groups another resource
// manages, such as an AKS cluster's node resource group, by upper-case
// ID, with the ID of the resource that manages each.
func managedResourceGroups(listed []azlist.AzureResource) map[string]string {
	groups := map[string]string{}
	for _, res := range listed {
		if _, ok := res.Id.(*armid.ResourceGroup); !ok {
			continue
		}
		if by, _ := res.Properties["managedBy"].(string); by != "" {
			groups[strings.ToUpper(res.Id.String())] = by
		}
	}
	return groups
}

// graphAttributes records what the catalogue's skip rules and import ID
// templates may use: the resource's name, the resource group it is in
// (or is), what manages that resource group (or the resource, for a
// resource group), and its tags, as Terraform flattens them,
// tags.<key>.
func graphAttributes(res azlist.AzureResource, managedGroups map[string]string) map[string]string {
	attributes := tagAttributes(res.Properties["tags"])
	attributes["name"] = lastSegment(res.Id.String())
	if rg, ok := res.Id.RootScope().(*armid.ResourceGroup); ok {
		attributes["resource_group_name"] = rg.Name
		if by := managedGroups[strings.ToUpper(rg.String())]; by != "" {
			attributes["resource_group_managed_by"] = by
		}
	}
	if by, _ := res.Properties["managedBy"].(string); by != "" {
		attributes["managed_by"] = by
	}
	return attributes
}

// catalogue is the catalogue of the import (see SetCatalogue), or the
// built-in one.
func (g *ResourceGraphLister) catalogue() *catalogue.Catalogue {
	if cat, ok := g.Settings()["catalogue"].(*catalogue.Catalogue); ok && cat != nil {
		return cat
	}
	return catalogue.MustBuiltin("azurerm")
}

// tagAttributes records a resource's tags as Terraform flattens them,
// tags.<key>, for selection rules and reports.
func tagAttributes(tags interface{}) map[string]string {
	attributes := map[string]string{}
	for k, v := range tagMap(tags) {
		attributes["tags."+k] = v
	}
	return attributes
}

// lastSegment is the name a resource ID ends with.
func lastSegment(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

// OptInServices are the services * leaves out (see
// resourceManagerServices).
func (p *Provider) OptInServices() []string {
	return slices.Clone(resourceManagerServices)
}

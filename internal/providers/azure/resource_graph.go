// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

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
// Manager, with Log Analytics workspaces' data sources of the kinds azurerm
// has types for (see workspaceDataSources). The catalogue's inventory
// entries map a resource to its azurerm type, else aztft does, with its
// import ID, reading what the ID alone can't say, as between a function
// app and a web app, from what the listing returned, and asking Resource
// Manager only for what it didn't (see graphResources and newLookup).
// With --azapi, what Resource Graph returned that azurerm has no type for
// is imported as azapi_resource instead of left out (see
// azapiCandidates). The catalogue's skip rules leave out what another resource manages
// (managedBy), such as an AKS cluster's nodes or a virtual machine's OS
// disk, but not a data disk attached to a machine, and what Azure creates
// itself, such as NetworkWatcherRG and its network watchers. What is in a
// group they leave out whole is listed without its child resources (see
// inSkippedGroup). A child list that fails such that resources may be
// missing, such as one refused or throttled, fails the service in part
// (see childListFailures): what was listed is imported, and the failure
// is reported.
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
	// The resource groups come from graphResourceGroups, in one query:
	// azlist would GET each group that holds a resource. Resources with a
	// managedBy are listed too: azlist would drop every attached disk,
	// whose managedBy is its virtual machine, while aztft imports the
	// machine's data disk attachments, which need the disks. The
	// catalogue's skip rules leave out what another resource owns (see
	// graphAttributes).
	lister, err := azlist.NewLister(listerOption(subscriptionID, credential, clientOptions))
	if err != nil {
		return err
	}
	withoutWorkspaceDataSources(lister.ARMSchemaTree)
	groups, err := graphResourceGroups(g.Context(), credential, &clientOptions, subscriptionID, predicate)
	if err != nil {
		return err
	}
	rows, err := lister.ListResourcesByQuery(g.Context(), predicate)
	if err != nil {
		return err
	}
	cat := g.catalogue()
	// What is in a group the default selection leaves out whole, such as
	// an AKS cluster's MC_* group, is listed, so that it can be selected,
	// but without its child resources, and the data sources of its
	// workspaces, which would take requests for what is left out anyway.
	// Naming the group with --resource-group lists them all.
	managedGroups := managedResourceGroups(groups)
	var parents, alone []azlist.AzureResource
	for _, res := range rows {
		if resourceGroup == "" && inSkippedGroup(res, managedGroups, cat) {
			alone = append(alone, res)
		} else {
			parents = append(parents, res)
		}
	}
	result, err := lister.ListRemaining(g.Context(), parents)
	if err != nil {
		return err
	}
	childErr := childListFailures(result.Errors)
	if err := g.Context().Err(); err != nil {
		return err
	}
	found := withResourceGroups(byID(slices.Concat(result.Resources, alone)), groups)
	// A partial failure keeps the data sources that were listed, and the
	// service fails in part (see childListFailures).
	sources, sourceTypes, sourceErr := workspaceDataSources(g.Context(), credential, &clientOptions, result.Resources)
	if sourceErr != nil && !harvest.IsPartial(sourceErr) {
		return sourceErr
	}
	found = append(found, sources...)
	query, sent := newLookup(g.Context(), credential, clientOptions, found)
	var viaAzapi map[string]bool
	if settingsOf(g.Settings()).azapi {
		viaAzapi = azapiCandidates(rows)
	}
	g.Resources = graphResources(found, cat, knownTypes(sourceTypes, query), viaAzapi)
	if err := g.Context().Err(); err != nil {
		return err
	}
	if g.Verbose() {
		log.Printf("azurerm: resource_graph: mapping %d listed resources to azurerm types took %d requests to Resource Manager", len(found), sent.Load())
	}
	if n := countType(g.Resources, azapiType); n > 0 {
		log.Printf("azurerm: resource_graph: %d resources azurerm has no type for come through the azapi provider, as %s (--azapi)", n, azapiType)
	}
	// Both are partial or nil; joined, they are partial only as wrapped.
	return harvest.Partial(errors.Join(childErr, sourceErr))
}

// childListParallel is how many child resource lists azlist has in flight:
// near the rate limit of a subscription, whatever the machine's CPUs, which
// azlist would take, leaving a 2-CPU runner far below the limit and
// starting dozens of waiting workers on a large one.
const childListParallel = 16

// listerOption is how resource_graph lists through azlist: recursively,
// with managed resources too, childListParallel lists at once.
func listerOption(subscriptionID string, credential azcore.TokenCredential, clientOptions arm.ClientOptions) azlist.Option {
	return azlist.Option{
		SubscriptionId: subscriptionID,
		Cred:           credential,
		ClientOpt:      clientOptions,
		Parallelism:    childListParallel,
		Recursive:      true,
		IncludeManaged: true,
	}
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

// inSkippedGroup reports whether the catalogue's rules for every type
// leave out res and anything under it for the resource group it is in
// alone, whatever its name, type or tags: as they leave out what is in a
// group another resource manages, such as an AKS cluster's MC_* group, or
// in a DefaultResourceGroup-<region>. managedGroups are the managed
// groups (see managedResourceGroups). The rules are asked about an ID
// under res, so a rule for res's own ID alone doesn't count.
func inSkippedGroup(res azlist.AzureResource, managedGroups map[string]string, cat *catalogue.Catalogue) bool {
	rg, ok := res.Id.RootScope().(*armid.ResourceGroup)
	if !ok {
		return false
	}
	attributes := map[string]string{"resource_group_name": rg.Name}
	if by := managedGroups[strings.ToUpper(rg.String())]; by != "" {
		attributes["resource_group_managed_by"] = by
	}
	return cat.Skipped(catalogue.AllTypes, res.Id.String()+"/", attributes) != ""
}

// byID sorts resources by ID, as azlist returns them, and returns them.
func byID(resources []azlist.AzureResource) []azlist.AzureResource {
	slices.SortFunc(resources, func(a, b azlist.AzureResource) int {
		return strings.Compare(a.Id.String(), b.Id.String())
	})
	return resources
}

// listStatus is the HTTP status of a failed list in the message of an
// azlist.ListError, as the Azure SDK's ResponseError writes it.
var listStatus = regexp.MustCompile(`RESPONSE (\d{3}):`)

// shapeErrors start the messages of the azlist.ListErrors for an item of
// a list that it couldn't read, such as one without an ID: azurerm has no
// type to import those by either.
var shapeErrors = []string{"marshalling ", "unmarshalling ", "no resource id found", "resource id is not a string", "parsing resource id "}

// childListFailures returns, joined as a partial error (see
// harvest.Partial), the failures among the child resource lists azlist
// reports that leave the listing incomplete, so that the service fails in
// part, keeping what it listed, rather than leaving resources out unseen:
// a list refused (401, 403), throttled (429), timed out (408) or failed
// by Resource Manager (5xx), or that got no answer at all. azlist leaves
// out the 404s of resources without children of a type; the other
// answers, such as 400 or 409 for a child type a resource's SKU or state
// doesn't have, 405 and 501 for types that can't be listed, and items it
// couldn't read, are logged, as nothing was left out that could be
// imported.
func childListFailures(errs []azlist.ListError) error {
	var failures []error
	for _, e := range errs {
		if childListFailed(e) {
			failures = append(failures, fmt.Errorf("azurerm: resource_graph: %w", e))
		} else {
			log.Printf("azurerm: resource_graph: %v", e)
		}
	}
	return harvest.Partial(errors.Join(failures...))
}

// childListFailed reports whether e left resources out (see
// childListFailures).
func childListFailed(e azlist.ListError) bool {
	m := listStatus.FindStringSubmatch(e.Message)
	if m == nil {
		return !slices.ContainsFunc(shapeErrors, func(prefix string) bool { return strings.HasPrefix(e.Message, prefix) })
	}
	status, _ := strconv.Atoi(m[1])
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	case http.StatusNotImplemented:
		return false
	}
	return status >= http.StatusInternalServerError
}

// queryType maps an Azure resource ID to its azurerm types and import IDs,
// and says whether the match is exact (see aztft.QueryTypeAndId).
type queryType func(id string) (types []aztft.Type, ids []string, exact bool, err error)

// graphResources turns what Resource Graph and Resource Manager listed
// into azurerm resources, in the order listed: through the catalogue's
// inventory entries, which override aztft; else from what the listing
// returned with the resource, where that decides its type (see
// fromListing), so a Log Analytics workspace's hundreds of tables cost no
// request; else through query, up to lookupParallel resources at once,
// one per azurerm type a resource maps to, such as a storage account and
// its queue properties. The attributes the catalogue's skip rules read
// are recorded first, from the listing, so a resource they leave out,
// such as a built-in table, is still recorded, with its type, for the
// report. Resources azurerm has no type for, or several it can't choose
// between, are logged and left out, but for those of viaAzapi (by
// upper-case ID; see azapiCandidates) that azurerm has no type for, which
// are recorded as azapi_resource (see azapiMapping).
func graphResources(listed []azlist.AzureResource, cat *catalogue.Catalogue, query queryType, viaAzapi map[string]bool) []harvest.Resource {
	owners := listedOwners{groups: managedResourceGroups(listed), osDisks: machineOSDisks(listed)}
	attributes := make([]map[string]string, len(listed))
	mapped := make([][]harvest.Resource, len(listed))
	var lookups []int
	for i, res := range listed {
		id := res.Id.String()
		attributes[i] = graphAttributes(res, owners)
		switch types := cat.FromInventory(res.Id.TypeString(), id); len(types) {
		case 0:
		case 1:
			mapped[i] = []harvest.Resource{harvest.Record(id, lastSegment(id), types[0], "azurerm", attributes[i])}
			continue
		default:
			log.Printf("azurerm: resource_graph: %s could be %s, says the catalogue; left out", id, strings.Join(types, " or "))
			continue
		}
		if m, ok := fromListing(res, attributes[i]); ok {
			mapped[i] = m.records(id, attributes[i])
			continue
		}
		lookups = append(lookups, i)
	}
	inParallel(lookups, func(i int) {
		id := listed[i].Id.String()
		m := fromQuery(id, query)
		if m.untyped && viaAzapi[strings.ToUpper(id)] {
			m = azapiMapping(listed[i].Id)
		}
		mapped[i] = m.records(id, attributes[i])
	})
	var resources []harvest.Resource
	for _, records := range mapped {
		resources = append(resources, records...)
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

// The Resource Manager types of a virtual machine, a managed disk and a
// network interface.
const (
	virtualMachineType   = "Microsoft.Compute/virtualMachines"
	managedDiskType      = "Microsoft.Compute/disks"
	networkInterfaceType = "Microsoft.Network/networkInterfaces"
)

// machineOSDisks returns the listed virtual machines whose OS disk is a
// managed disk, by upper-case ID, with the upper-case ID of that disk,
// as the machine's storageProfile names it.
func machineOSDisks(listed []azlist.AzureResource) map[string]string {
	disks := map[string]string{}
	for _, res := range listed {
		if !strings.EqualFold(res.Id.TypeString(), virtualMachineType) {
			continue
		}
		if disk := nestedString(res.Properties, "properties", "storageProfile", "osDisk", "managedDisk", "id"); disk != "" {
			disks[strings.ToUpper(res.Id.String())] = strings.ToUpper(disk)
		}
	}
	return disks
}

// nestedString returns the string at path in properties, or "" where
// there is none.
func nestedString(properties map[string]interface{}, path ...string) string {
	for _, key := range path[:len(path)-1] {
		properties, _ = properties[key].(map[string]interface{})
	}
	s, _ := properties[path[len(path)-1]].(string)
	return s
}

// listedOwners is what graphAttributes reads of the other listed
// resources: the resource groups another resource manages, with what
// manages each (see managedResourceGroups), and the OS disks of virtual
// machines (see machineOSDisks).
type listedOwners struct {
	groups  map[string]string
	osDisks map[string]string
}

// graphAttributes records what the catalogue's skip rules and import ID
// templates may use: the resource's name, the resource group it is in
// (or is), what manages that resource group (or the resource:
// managed_by), and its tags, as Terraform flattens them, tags.<key>.
//
// A managed disk's managedBy is the virtual machine it is attached to. Of
// the machine's OS disk, which the machine's os_disk block manages, it is
// recorded as managed_by, with os_disk_of; of a data disk, which is a
// resource of its own that an azurerm_virtual_machine_data_disk_attachment
// attaches, as attached_to, not managed_by.
func graphAttributes(res azlist.AzureResource, owners listedOwners) map[string]string {
	attributes := tagAttributes(res.Properties["tags"])
	attributes["name"] = lastSegment(res.Id.String())
	if rg, ok := res.Id.RootScope().(*armid.ResourceGroup); ok {
		attributes["resource_group_name"] = rg.Name
		if by := owners.groups[strings.ToUpper(rg.String())]; by != "" {
			attributes["resource_group_managed_by"] = by
		}
	}
	if by, _ := res.Properties["managedBy"].(string); by != "" {
		switch diskRole(res, by, owners.osDisks) {
		case dataDisk:
			attributes["attached_to"] = by
		case osDisk:
			attributes["managed_by"] = by
			attributes["os_disk_of"] = by
		default:
			attributes["managed_by"] = by
		}
	}
	builtInAttributes(res, attributes)
	return attributes
}

// diskKind is what a managed disk is to the virtual machine that its
// managedBy names.
type diskKind int

const (
	notMachineDisk diskKind = iota
	osDisk
	dataDisk
)

// diskRole tells whether res is the OS disk or a data disk of the
// virtual machine by, its managedBy: from the machine's storageProfile
// when the machine was listed with one (osDisks, see machineOSDisks),
// else from the disk's osType, which only an OS disk has. It returns
// notMachineDisk for a resource that isn't a managed disk, or one that
// something other than a virtual machine manages.
func diskRole(res azlist.AzureResource, by string, osDisks map[string]string) diskKind {
	if !strings.EqualFold(res.Id.TypeString(), managedDiskType) {
		return notMachineDisk
	}
	machine, err := armid.ParseResourceId(by)
	if err != nil || !strings.EqualFold(machine.TypeString(), virtualMachineType) {
		return notMachineDisk
	}
	if disk, ok := osDisks[strings.ToUpper(machine.String())]; ok {
		if disk == strings.ToUpper(res.Id.String()) {
			return osDisk
		}
		return dataDisk
	}
	if nestedString(res.Properties, "properties", "osType") != "" {
		return osDisk
	}
	return dataDisk
}

// The Resource Manager types of a Log Analytics workspace's tables and
// saved searches: every workspace has hundreds of each built in (see
// builtInAttributes).
const (
	workspaceTableType       = "Microsoft.OperationalInsights/workspaces/tables"
	workspaceSavedSearchType = "Microsoft.OperationalInsights/workspaces/savedSearches"
)

// builtInAttributes records in attributes what the catalogue's skip rules
// need to tell what every Log Analytics workspace has built in from what
// its owner made or changed, from the properties Resource Manager listed
// the resource with:
//
//   - of a table: its type (table_type: Microsoft for a built-in table,
//     CustomLog for a custom one), its plan, and whether its retention
//     and total retention are the workspace's defaults
//     (retention_in_days_as_default, total_retention_in_days_as_default).
//     A built-in table on the Analytics plan, with both by default, is as
//     Azure made it.
//   - of a saved search a solution added, as its name is
//     <solution>(<workspace>)_..., such as the LogManagement searches
//     every workspace has: the solution, and from_solution "true".
//
// and what tells a resource another one creates and deletes with itself:
//
//   - of a network interface: the private endpoint (private_endpoint) or
//     Private Link service (private_link_service) that created it.
//
// Other resources get nothing.
func builtInAttributes(res azlist.AzureResource, attributes map[string]string) {
	properties, _ := res.Properties["properties"].(map[string]interface{})
	switch typ := res.Id.TypeString(); {
	case strings.EqualFold(typ, networkInterfaceType):
		for property, attribute := range map[string]string{
			"privateEndpoint":    "private_endpoint",
			"privateLinkService": "private_link_service",
		} {
			if id := nestedString(properties, property, "id"); id != "" {
				attributes[attribute] = id
			}
		}
	case strings.EqualFold(typ, workspaceTableType):
		if schema, ok := properties["schema"].(map[string]interface{}); ok {
			if tableType, ok := schema["tableType"].(string); ok {
				attributes["table_type"] = tableType
			}
		}
		if plan, ok := properties["plan"].(string); ok {
			attributes["plan"] = plan
		}
		for property, attribute := range map[string]string{
			"retentionInDaysAsDefault":      "retention_in_days_as_default",
			"totalRetentionInDaysAsDefault": "total_retention_in_days_as_default",
		} {
			if asDefault, ok := properties[property].(bool); ok {
				attributes[attribute] = strconv.FormatBool(asDefault)
			}
		}
	case strings.EqualFold(typ, workspaceSavedSearchType):
		if names := res.Id.Names(); len(names) == 2 {
			if solution := savedSearchSolution(names[0], names[1]); solution != "" {
				attributes["solution"] = solution
				attributes["from_solution"] = "true"
			}
		}
	}
}

// savedSearchSolution returns the solution that added the saved search
// name to workspace, which names its searches
// <solution>(<workspace>)_<category>|<search>, or "" for a search named
// otherwise, such as one a user saved. The workspace's name must match,
// whatever its case, so a user's search that only looks like that isn't
// taken for a solution's.
func savedSearchSolution(workspace, name string) string {
	solution, rest, ok := strings.Cut(name, "(")
	if !ok || solution == "" || strings.ContainsFunc(solution, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		return ""
	}
	prefix := workspace + ")_"
	if len(rest) <= len(prefix) || !strings.EqualFold(rest[:len(prefix)], prefix) {
		return ""
	}
	return solution
}

// catalogue is the catalogue of the import (see SetCatalogue), or the
// built-in one.
func (g *ResourceGraphLister) catalogue() *catalogue.Catalogue {
	if cat := settingsOf(g.Settings()).catalogue; cat != nil {
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

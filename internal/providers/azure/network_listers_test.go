// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// armItem is the JSON of an ARM resource with an ID, a name and, unless
// properties is empty, the given properties object.
func armItem(id, name, properties string) string {
	item := `{"id":"` + id + `","name":"` + name + `"`
	if properties != "" {
		item += `,"properties":` + properties
	}
	return item + "}"
}

// armList is the JSON of one page of a Resource Manager list operation.
func armList(items ...string) string {
	return `{"value":[` + strings.Join(items, ",") + `]}`
}

// listedResource is what a test checks of a listed resource.
type listedResource struct {
	Type, ID, Name string
	Attributes     map[string]string
}

// listedResources summarises resources, with an empty attribute map read
// as none.
func listedResources(resources []harvest.Resource) []listedResource {
	var listed []listedResource
	for _, r := range resources {
		attributes := r.Observed.Attributes
		if len(attributes) == 0 {
			attributes = nil
		}
		listed = append(listed, listedResource{
			Type:       r.Address.Type,
			ID:         r.Observed.ID,
			Name:       r.RawName,
			Attributes: attributes,
		})
	}
	return listed
}

// --resource-group limits virtual networks to that resource group;
// without it the whole subscription is listed.
func TestVirtualNetworkListsTheResourceGroup(t *testing.T) {
	t.Parallel()
	const network = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/virtualNetworks/net1"
	for resourceGroup, wantPath := range map[string]string{
		"rg1": "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/virtualNetworks",
		"":    "/subscriptions/sub/providers/Microsoft.Network/virtualNetworks",
	} {
		transport := providertest.New(answering(armList(armItem(network, "net1", ""))))
		g := &VirtualNetworkLister{}
		g.SetSettings(fakeArgs(transport, resourceGroup))

		resources, err := g.List(t.Context(), harvest.Settings{})
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(transport.Paths(), []string{wantPath}) {
			t.Errorf("resource group %q: requested %v, want %s", resourceGroup, transport.Paths(), wantPath)
		}
		want := []listedResource{{Type: "azurerm_virtual_network", ID: network, Name: "net1"}}
		if got := listedResources(resources); !reflect.DeepEqual(got, want) {
			t.Errorf("resource group %q: got %+v, want %+v", resourceGroup, got, want)
		}
	}
}

// An interface a private endpoint or a Private Link service created is
// recorded with it, as resource_graph records it; the others bare.
func TestNetworkInterfaceRecordsItsEndpoint(t *testing.T) {
	t.Parallel()
	const network = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
	transport := providertest.New(answering(armList(
		armItem(network+"networkInterfaces/pe.nic", "pe.nic", `{"privateEndpoint":{"id":"`+network+`privateEndpoints/pe"}}`),
		armItem(network+"networkInterfaces/pls.nic", "pls.nic", `{"privateLinkService":{"id":"`+network+`privateLinkServices/pls"}}`),
		armItem(network+"networkInterfaces/vm1-nic", "vm1-nic", `{"ipConfigurations":[]}`),
	)))
	g := &NetworkInterfaceLister{}
	g.SetSettings(fakeArgs(transport, "rg1"))
	resources, err := g.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	want := []listedResource{
		{Type: "azurerm_network_interface", ID: network + "networkInterfaces/pe.nic", Name: "pe.nic", Attributes: map[string]string{"private_endpoint": network + "privateEndpoints/pe"}},
		{Type: "azurerm_network_interface", ID: network + "networkInterfaces/pls.nic", Name: "pls.nic", Attributes: map[string]string{"private_link_service": network + "privateLinkServices/pls"}},
		{Type: "azurerm_network_interface", ID: network + "networkInterfaces/vm1-nic", Name: "vm1-nic"},
	}
	if got := listedResources(resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Every lister asks for the resource-group-scoped list with
// --resource-group (floci-az has no other) and for the subscription-wide one
// without it, and lists children under the parent's own resource group.
func TestListersHonourTheResourceGroup(t *testing.T) {
	t.Parallel()
	const (
		scoped = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
		all    = "/subscriptions/sub/providers/Microsoft.Network/"
		other  = "/subscriptions/sub/resourceGroups/rg2/providers/Microsoft.Network/"
	)
	// In the subscription-wide cases the parents live in rg2, so their
	// children must be asked for there.
	for _, tc := range []struct {
		service       string
		lister        settledLister
		resourceGroup string
		bodies        map[string]string
		wantPaths     []string
		wantTypes     []string
	}{
		{
			service: "resource_group", lister: &ResourceGroupLister{}, resourceGroup: "rg1",
			bodies:    map[string]string{"/subscriptions/sub/resourcegroups/rg1": armItem("/subscriptions/sub/resourceGroups/rg1", "rg1", "")},
			wantPaths: []string{"/subscriptions/sub/resourcegroups/rg1"},
			wantTypes: []string{"azurerm_resource_group"},
		},
		{
			service: "resource_group", lister: &ResourceGroupLister{},
			bodies: map[string]string{"/subscriptions/sub/resourcegroups": armList(
				armItem("/subscriptions/sub/resourceGroups/rg1", "rg1", ""),
				armItem("/subscriptions/sub/resourceGroups/rg2", "rg2", ""))},
			wantPaths: []string{"/subscriptions/sub/resourcegroups"},
			wantTypes: []string{"azurerm_resource_group", "azurerm_resource_group"},
		},
		{
			service: "subnet", lister: &SubnetLister{}, resourceGroup: "rg1",
			bodies: map[string]string{
				scoped + "virtualNetworks":              armList(armItem(scoped+"virtualNetworks/net1", "net1", "")),
				scoped + "virtualNetworks/net1/subnets": armList(armItem(scoped+"virtualNetworks/net1/subnets/app", "app", "")),
				scoped + "serviceEndpointPolicies":      armList(),
			},
			wantPaths: []string{scoped + "virtualNetworks", scoped + "virtualNetworks/net1/subnets", scoped + "serviceEndpointPolicies"},
			wantTypes: []string{"azurerm_subnet"},
		},
		{
			service: "subnet", lister: &SubnetLister{},
			bodies: map[string]string{
				all + "virtualNetworks":                armList(armItem(other+"virtualNetworks/net1", "net1", "")),
				other + "virtualNetworks/net1/subnets": armList(armItem(other+"virtualNetworks/net1/subnets/app", "app", "")),
				all + "serviceEndpointPolicies":        armList(),
			},
			wantPaths: []string{all + "virtualNetworks", other + "virtualNetworks/net1/subnets", all + "serviceEndpointPolicies"},
			wantTypes: []string{"azurerm_subnet"},
		},
		{
			service: "network_security_group", lister: &NetworkSecurityGroupLister{}, resourceGroup: "rg1",
			bodies: map[string]string{
				scoped + "networkSecurityGroups":                   armList(armItem(scoped+"networkSecurityGroups/web", "web", "")),
				scoped + "networkSecurityGroups/web/securityRules": armList(),
			},
			wantPaths: []string{scoped + "networkSecurityGroups", scoped + "networkSecurityGroups/web/securityRules"},
			wantTypes: []string{"azurerm_network_security_group"},
		},
		{
			service: "network_security_group", lister: &NetworkSecurityGroupLister{},
			bodies: map[string]string{
				all + "networkSecurityGroups":                     armList(armItem(other+"networkSecurityGroups/web", "web", "")),
				other + "networkSecurityGroups/web/securityRules": armList(),
			},
			wantPaths: []string{all + "networkSecurityGroups", other + "networkSecurityGroups/web/securityRules"},
			wantTypes: []string{"azurerm_network_security_group"},
		},
		{
			service: "public_ip", lister: &PublicIPLister{}, resourceGroup: "rg1",
			bodies: map[string]string{
				scoped + "publicIPAddresses": armList(armItem(scoped+"publicIPAddresses/ip1", "ip1", "")),
				scoped + "publicIPPrefixes":  armList(armItem(scoped+"publicIPPrefixes/prefix1", "prefix1", "")),
			},
			wantPaths: []string{scoped + "publicIPAddresses", scoped + "publicIPPrefixes"},
			wantTypes: []string{"azurerm_public_ip", "azurerm_public_ip_prefix"},
		},
		{
			service: "public_ip", lister: &PublicIPLister{},
			bodies: map[string]string{
				all + "publicIPAddresses": armList(armItem(other+"publicIPAddresses/ip1", "ip1", "")),
				all + "publicIPPrefixes":  armList(),
			},
			wantPaths: []string{all + "publicIPAddresses", all + "publicIPPrefixes"},
			wantTypes: []string{"azurerm_public_ip"},
		},
		{
			service: "network_interface", lister: &NetworkInterfaceLister{}, resourceGroup: "rg1",
			bodies:    map[string]string{scoped + "networkInterfaces": armList(armItem(scoped+"networkInterfaces/nic1", "nic1", ""))},
			wantPaths: []string{scoped + "networkInterfaces"},
			wantTypes: []string{"azurerm_network_interface"},
		},
		{
			service: "network_interface", lister: &NetworkInterfaceLister{},
			bodies:    map[string]string{all + "networkInterfaces": armList(armItem(other+"networkInterfaces/nic1", "nic1", ""))},
			wantPaths: []string{all + "networkInterfaces"},
			wantTypes: []string{"azurerm_network_interface"},
		},
	} {
		transport := routed(tc.bodies)
		tc.lister.SetSettings(fakeArgs(transport, tc.resourceGroup))

		listed, err := tc.lister.List(t.Context(), harvest.Settings{})
		if err != nil {
			t.Errorf("%s, resource group %q: %v", tc.service, tc.resourceGroup, err)
			continue
		}

		if !reflect.DeepEqual(transport.Paths(), tc.wantPaths) {
			t.Errorf("%s, resource group %q: requested %v, want %v", tc.service, tc.resourceGroup, transport.Paths(), tc.wantPaths)
		}
		var types []string
		for _, r := range listed {
			types = append(types, r.Address.Type)
		}
		if !reflect.DeepEqual(types, tc.wantTypes) {
			t.Errorf("%s, resource group %q: listed %v, want %v", tc.service, tc.resourceGroup, types, tc.wantTypes)
		}
	}
}

// A --resource-group that doesn't exist fails the resource_group service
// rather than listing nothing.
func TestResourceGroupThatIsMissingIsAnError(t *testing.T) {
	t.Parallel()
	transport := routed(nil)
	g := &ResourceGroupLister{}
	g.SetSettings(fakeArgs(transport, "missing"))

	resources, err := g.List(t.Context(), harvest.Settings{})
	if err == nil {
		t.Errorf("listed %v, want an error", resources)
	}
}

// A page of the subscription's resource groups that fails keeps the groups
// listed before it and fails the listing in part, so that the groups it
// left out aren't taken for deleted; a first page that fails lists nothing
// and fails the listing in full.
func TestResourceGroupPageThatFailsIsPartial(t *testing.T) {
	t.Parallel()
	const (
		groups = "/subscriptions/sub/resourcegroups"
		next   = groups + "/next"
	)
	transport := providertest.New(armRoutes(map[string]string{groups: `{"value":[` + armItem("/subscriptions/sub/resourceGroups/rg1", "rg1", "") + `],"nextLink":"https://management.azure.com` + next + `"}`}, map[string]int{next: http.StatusForbidden}))
	g := &ResourceGroupLister{}
	g.SetSettings(fakeArgs(transport, ""))
	resources, err := g.List(t.Context(), harvest.Settings{})
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "AuthorizationFailed") {
		t.Errorf("a later page failing: got %v, want the refusal as a partial error", err)
	}
	want := []listedResource{{Type: "azurerm_resource_group", ID: "/subscriptions/sub/resourceGroups/rg1", Name: "rg1"}}
	if got := listedResources(resources); !reflect.DeepEqual(got, want) {
		t.Errorf("a later page failing: listed %+v, want %+v", got, want)
	}

	transport = providertest.New(armRoutes(nil, map[string]int{groups: http.StatusForbidden}))
	g = &ResourceGroupLister{}
	g.SetSettings(fakeArgs(transport, ""))
	resources, err = g.List(t.Context(), harvest.Settings{})
	if err == nil || harvest.IsPartial(err) || len(resources) != 0 {
		t.Errorf("the first page failing: got %v, listed %v, want a full failure", err, resources)
	}
}

// A subnet's links to a route table, a security group and a NAT gateway
// become association resources imported by the subnet's ID; a subnet
// without links, or with a link that has no ID, gets none; a subnet without
// a name is skipped. Service endpoint policies follow the subnets.
func TestSubnetListsAssociationsAndPolicies(t *testing.T) {
	t.Parallel()
	const (
		rg         = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
		network    = rg + "virtualNetworks/net1"
		app        = network + "/subnets/app"
		plain      = network + "/subnets/plain"
		bare       = network + "/subnets/bare"
		routeTable = rg + "routeTables/routes"
		nsg        = rg + "networkSecurityGroups/web"
		nat        = rg + "natGateways/nat"
		storage    = rg + "serviceEndpointPolicies/storage"
	)
	transport := routed(map[string]string{
		rg + "virtualNetworks": armList(armItem(network, "net1", "")),
		network + "/subnets": armList(
			armItem(plain, "plain", `{"addressPrefix":"10.0.0.0/24"}`),
			armItem(app, "app", `{"routeTable":{"id":"`+routeTable+`"},"networkSecurityGroup":{"id":"`+nsg+`"},"natGateway":{"id":"`+nat+`"}}`),
			armItem(bare, "bare", `{"routeTable":{}}`),
			`{"id":"`+network+`/subnets/nameless"}`,
		),
		rg + "serviceEndpointPolicies": armList(armItem(storage, "storage", "")),
	})
	g := &SubnetLister{}
	g.SetSettings(fakeArgs(transport, "rg1"))

	resources, err := g.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	want := []listedResource{
		{Type: "azurerm_subnet", ID: plain, Name: "plain"},
		{Type: "azurerm_subnet", ID: app, Name: "app"},
		{
			Type: "azurerm_subnet_route_table_association", ID: app, Name: "app_route_table_association",
			Attributes: map[string]string{"subnet_id": app, "route_table_id": routeTable},
		},
		{
			Type: "azurerm_subnet_network_security_group_association", ID: app, Name: "app_network_security_group_association",
			Attributes: map[string]string{"subnet_id": app, "network_security_group_id": nsg},
		},
		{
			Type: "azurerm_subnet_nat_gateway_association", ID: app, Name: "app_nat_gateway_association",
			Attributes: map[string]string{"subnet_id": app, "nat_gateway_id": nat},
		},
		{Type: "azurerm_subnet", ID: bare, Name: "bare"},
		{Type: "azurerm_subnet_service_endpoint_storage_policy", ID: storage, Name: "storage"},
	}
	if got := listedResources(resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Each security group's rules are listed under the group's own resource
// group and labelled "<group>_<rule>", so rules of the same name in two
// groups stay apart.
func TestNetworkSecurityGroupListsRules(t *testing.T) {
	t.Parallel()
	const (
		groupA = "/subscriptions/sub/resourceGroups/rg-a/providers/Microsoft.Network/networkSecurityGroups/web"
		groupB = "/subscriptions/sub/resourceGroups/rg-b/providers/Microsoft.Network/networkSecurityGroups/db"
	)
	transport := routed(map[string]string{
		"/subscriptions/sub/providers/Microsoft.Network/networkSecurityGroups": armList(
			armItem(groupA, "web", ""),
			armItem(groupB, "db", "")),
		groupA + "/securityRules": armList(
			armItem(groupA+"/securityRules/https", "https", `{"priority":100}`),
			armItem(groupA+"/securityRules/ssh", "ssh", `{"priority":200}`)),
		groupB + "/securityRules": armList(armItem(groupB+"/securityRules/https", "https", "")),
	})
	g := &NetworkSecurityGroupLister{}
	g.SetSettings(fakeArgs(transport, ""))

	resources, err := g.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	want := []listedResource{
		{Type: "azurerm_network_security_group", ID: groupA, Name: "web"},
		{Type: "azurerm_network_security_rule", ID: groupA + "/securityRules/https", Name: "web_https"},
		{Type: "azurerm_network_security_rule", ID: groupA + "/securityRules/ssh", Name: "web_ssh"},
		{Type: "azurerm_network_security_group", ID: groupB, Name: "db"},
		{Type: "azurerm_network_security_rule", ID: groupB + "/securityRules/https", Name: "db_https"},
	}
	if got := listedResources(resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A rule listing that fails fails the whole service.
func TestNetworkSecurityGroupFailsWhenRulesFail(t *testing.T) {
	t.Parallel()
	const group = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/networkSecurityGroups/web"
	transport := routed(map[string]string{
		"/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/networkSecurityGroups": armList(armItem(group, "web", "")),
	})
	g := &NetworkSecurityGroupLister{}
	g.SetSettings(fakeArgs(transport, "rg1"))

	resources, err := g.List(t.Context(), harvest.Settings{})
	if err == nil {
		t.Errorf("listed %v, want an error", resources)
	}
}

// The Resource Manager listers record the tags their list responses carry,
// tags.<key>, as resource_graph records Resource Graph's, so that the
// provider needs no second read of them; a tag without a value is left
// out, and an untagged resource has no attributes.
func TestResourceManagerListersRecordTags(t *testing.T) {
	t.Parallel()
	const (
		group   = "/subscriptions/sub/resourceGroups/rg1"
		network = group + "/providers/Microsoft.Network/"
	)
	for _, tc := range []struct {
		name   string
		lister settledLister
		body   string
		want   []listedResource
	}{
		{
			name:   "resource group",
			lister: &ResourceGroupLister{},
			body:   `{"id":"` + group + `","name":"rg1","tags":{"env":"prod","empty":null}}`,
			want:   []listedResource{{Type: "azurerm_resource_group", ID: group, Name: "rg1", Attributes: map[string]string{"tags.env": "prod"}}},
		},
		{
			name:   "virtual networks",
			lister: &VirtualNetworkLister{},
			body: armList(
				`{"id":"`+network+`virtualNetworks/net1","name":"net1","tags":{"team":"net","cost centre":"42"}}`,
				armItem(network+"virtualNetworks/net2", "net2", ""),
			),
			want: []listedResource{
				{Type: "azurerm_virtual_network", ID: network + "virtualNetworks/net1", Name: "net1", Attributes: map[string]string{"tags.team": "net", "tags.cost centre": "42"}},
				{Type: "azurerm_virtual_network", ID: network + "virtualNetworks/net2", Name: "net2"},
			},
		},
		{
			name:   "a network interface with its endpoint",
			lister: &NetworkInterfaceLister{},
			body:   armList(`{"id":"` + network + `networkInterfaces/pe.nic","name":"pe.nic","tags":{"team":"net"},"properties":{"privateEndpoint":{"id":"` + network + `privateEndpoints/pe"}}}`),
			want: []listedResource{{Type: "azurerm_network_interface", ID: network + "networkInterfaces/pe.nic", Name: "pe.nic", Attributes: map[string]string{
				"private_endpoint": network + "privateEndpoints/pe",
				"tags.team":        "net",
			}}},
		},
	} {
		transport := providertest.New(answering(tc.body))
		tc.lister.SetSettings(fakeArgs(transport, "rg1"))
		listed, err := tc.lister.List(t.Context(), harvest.Settings{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := listedResources(listed); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Rules and subnets that come inline in the parent's list are used as they
// are, an empty list too: no list is asked for per security group or per
// network. Only a parent whose list lacks the field, as floci-az answers, is
// asked.
func TestNetworkListersUseInlineChildren(t *testing.T) {
	t.Parallel()
	const (
		rg         = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
		web        = rg + "networkSecurityGroups/web"
		empty      = rg + "networkSecurityGroups/empty"
		unset      = rg + "networkSecurityGroups/unset"
		bare       = rg + "networkSecurityGroups/bare"
		network    = rg + "virtualNetworks/net1"
		app        = network + "/subnets/app"
		nsg        = rg + "networkSecurityGroups/web"
		noSubnets  = rg + "virtualNetworks/none"
		unlisted   = rg + "virtualNetworks/unlisted"
		bareNet    = rg + "virtualNetworks/bare"
		standalone = unlisted + "/subnets/db"
	)
	groups := routed(map[string]string{
		rg + "networkSecurityGroups": armList(
			armItem(web, "web", `{"securityRules":[`+armItem(web+"/securityRules/https", "https", `{"priority":100}`)+`],"defaultSecurityRules":[`+armItem(web+"/defaultSecurityRules/AllowVnetInBound", "AllowVnetInBound", "")+`]}`),
			armItem(empty, "empty", `{"securityRules":[]}`),
			armItem(unset, "unset", `{"provisioningState":"Succeeded"}`),
			armItem(bare, "bare", ""),
		),
		unset + "/securityRules": armList(),
		bare + "/securityRules":  armList(armItem(bare+"/securityRules/ssh", "ssh", "")),
	})
	g := &NetworkSecurityGroupLister{}
	g.SetSettings(fakeArgs(groups, "rg1"))
	resources, err := g.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{rg + "networkSecurityGroups", unset + "/securityRules", bare + "/securityRules"}; !reflect.DeepEqual(groups.Paths(), want) {
		t.Errorf("security groups: requested %v, want %v", groups.Paths(), want)
	}
	wantGroups := []listedResource{
		{Type: "azurerm_network_security_group", ID: web, Name: "web"},
		{Type: "azurerm_network_security_rule", ID: web + "/securityRules/https", Name: "web_https"},
		{Type: "azurerm_network_security_group", ID: empty, Name: "empty"},
		{Type: "azurerm_network_security_group", ID: unset, Name: "unset"},
		{Type: "azurerm_network_security_group", ID: bare, Name: "bare"},
		{Type: "azurerm_network_security_rule", ID: bare + "/securityRules/ssh", Name: "bare_ssh"},
	}
	if got := listedResources(resources); !reflect.DeepEqual(got, wantGroups) {
		t.Errorf("security groups: got %+v\nwant %+v", got, wantGroups)
	}

	networks := routed(map[string]string{
		rg + "virtualNetworks": armList(
			armItem(network, "net1", `{"subnets":[`+armItem(app, "app", `{"networkSecurityGroup":{"id":"`+nsg+`"}}`)+`]}`),
			armItem(noSubnets, "none", `{"subnets":[]}`),
			armItem(unlisted, "unlisted", `{"addressSpace":{"addressPrefixes":["10.1.0.0/16"]}}`),
			armItem(bareNet, "bare", ""),
		),
		unlisted + "/subnets":          armList(armItem(standalone, "db", "")),
		bareNet + "/subnets":           armList(),
		rg + "serviceEndpointPolicies": armList(),
	})
	s := &SubnetLister{}
	s.SetSettings(fakeArgs(networks, "rg1"))
	resources, err = s.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{rg + "virtualNetworks", unlisted + "/subnets", bareNet + "/subnets", rg + "serviceEndpointPolicies"}; !reflect.DeepEqual(networks.Paths(), want) {
		t.Errorf("subnets: requested %v, want %v", networks.Paths(), want)
	}
	wantSubnets := []listedResource{
		{Type: "azurerm_subnet", ID: app, Name: "app"},
		{
			Type: "azurerm_subnet_network_security_group_association", ID: app, Name: "app_network_security_group_association",
			Attributes: map[string]string{"subnet_id": app, "network_security_group_id": nsg},
		},
		{Type: "azurerm_subnet", ID: standalone, Name: "db"},
	}
	if got := listedResources(resources); !reflect.DeepEqual(got, wantSubnets) {
		t.Errorf("subnets: got %+v\nwant %+v", got, wantSubnets)
	}
}

// In a custom cloud (ARM_ENVIRONMENT=stack, as floci-az runs) an empty list
// of rules or subnets is asked again, as such emulators answer one for
// children created on their own; in Azure's own clouds it isn't (above).
func TestNetworkListersAskAgainForEmptyChildrenInACustomCloud(t *testing.T) {
	t.Parallel()
	const (
		rg      = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
		group   = rg + "networkSecurityGroups/empty"
		network = rg + "virtualNetworks/net1"
		subnet  = network + "/subnets/db"
	)
	custom := func(transport *providertest.Cloud) serviceSettings {
		args := fakeArgs(transport, "rg1")
		options := args.clientOptions
		options.Cloud = cloud.Configuration{
			ActiveDirectoryAuthorityHost: "https://login.floci.test/",
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {Endpoint: "https://management.floci.test", Audience: "https://management.floci.test"},
			},
		}
		return args
	}
	groups := routed(map[string]string{
		rg + "networkSecurityGroups": armList(armItem(group, "empty", `{"securityRules":[]}`)),
		group + "/securityRules":     armList(armItem(group+"/securityRules/ssh", "ssh", "")),
	})
	g := &NetworkSecurityGroupLister{}
	g.SetSettings(custom(groups))
	_, err := g.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{rg + "networkSecurityGroups", group + "/securityRules"}; !reflect.DeepEqual(groups.Paths(), want) {
		t.Errorf("security groups: requested %v, want %v", groups.Paths(), want)
	}
	networks := routed(map[string]string{
		rg + "virtualNetworks":         armList(armItem(network, "net1", `{"subnets":[]}`)),
		network + "/subnets":           armList(armItem(subnet, "db", "")),
		rg + "serviceEndpointPolicies": armList(),
	})
	s := &SubnetLister{}
	s.SetSettings(custom(networks))
	resources, err := s.List(t.Context(), harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := listedResources(resources), []listedResource{{Type: "azurerm_subnet", ID: subnet, Name: "db"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("subnets: got %+v, want %+v", got, want)
	}
}

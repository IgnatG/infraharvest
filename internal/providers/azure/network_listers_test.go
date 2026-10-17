// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// routedTransport answers each request with the body registered for its
// path, or with a Resource Manager 404 for a path nobody registered, and
// records the paths asked for in order.
type routedTransport struct {
	bodies map[string]string
	paths  []string
}

func (f *routedTransport) Do(r *http.Request) (*http.Response, error) {
	f.paths = append(f.paths, r.URL.Path)
	status := http.StatusOK
	body, ok := f.bodies[r.URL.Path]
	if !ok {
		status = http.StatusNotFound
		body = `{"error":{"code":"NotFound","message":"nothing at ` + r.URL.Path + `"}}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// routedArgs is fakeArgs for a routedTransport.
func routedArgs(transport *routedTransport, resourceGroup string) map[string]interface{} {
	return map[string]interface{}{
		"subscription_id": "sub",
		"resource_group":  resourceGroup,
		"credential":      azcore.TokenCredential(fakeCredential{}),
		"client_options":  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
	}
}

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
	const network = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/virtualNetworks/net1"
	for resourceGroup, wantPath := range map[string]string{
		"rg1": "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/virtualNetworks",
		"":    "/subscriptions/sub/providers/Microsoft.Network/virtualNetworks",
	} {
		transport := &fakeTransport{body: armList(armItem(network, "net1", ""))}
		g := &VirtualNetworkLister{}
		g.Configure(harvest.ListerConfig{Settings: fakeArgs(transport, resourceGroup)})

		if err := g.List(); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(transport.paths, []string{wantPath}) {
			t.Errorf("resource group %q: requested %v, want %s", resourceGroup, transport.paths, wantPath)
		}
		want := []listedResource{{Type: "azurerm_virtual_network", ID: network, Name: "net1"}}
		if got := listedResources(g.Resources); !reflect.DeepEqual(got, want) {
			t.Errorf("resource group %q: got %+v, want %+v", resourceGroup, got, want)
		}
	}
}

// An interface a private endpoint or a Private Link service created is
// recorded with it, as resource_graph records it; the others bare.
func TestNetworkInterfaceRecordsItsEndpoint(t *testing.T) {
	const network = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
	transport := &fakeTransport{body: armList(
		armItem(network+"networkInterfaces/pe.nic", "pe.nic", `{"privateEndpoint":{"id":"`+network+`privateEndpoints/pe"}}`),
		armItem(network+"networkInterfaces/pls.nic", "pls.nic", `{"privateLinkService":{"id":"`+network+`privateLinkServices/pls"}}`),
		armItem(network+"networkInterfaces/vm1-nic", "vm1-nic", `{"ipConfigurations":[]}`),
	)}
	g := &NetworkInterfaceLister{}
	g.Configure(harvest.ListerConfig{Settings: fakeArgs(transport, "rg1")})
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	want := []listedResource{
		{Type: "azurerm_network_interface", ID: network + "networkInterfaces/pe.nic", Name: "pe.nic", Attributes: map[string]string{"private_endpoint": network + "privateEndpoints/pe"}},
		{Type: "azurerm_network_interface", ID: network + "networkInterfaces/pls.nic", Name: "pls.nic", Attributes: map[string]string{"private_link_service": network + "privateLinkServices/pls"}},
		{Type: "azurerm_network_interface", ID: network + "networkInterfaces/vm1-nic", Name: "vm1-nic"},
	}
	if got := listedResources(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Every lister asks for the resource-group-scoped list with
// --resource-group (floci-az has no other) and for the subscription-wide one
// without it, and lists children under the parent's own resource group.
func TestListersHonourTheResourceGroup(t *testing.T) {
	const (
		scoped = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
		all    = "/subscriptions/sub/providers/Microsoft.Network/"
		other  = "/subscriptions/sub/resourceGroups/rg2/providers/Microsoft.Network/"
	)
	// In the subscription-wide cases the parents live in rg2, so their
	// children must be asked for there.
	for _, tc := range []struct {
		service       string
		lister        harvest.Lister
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
		transport := &routedTransport{bodies: tc.bodies}
		tc.lister.Configure(harvest.ListerConfig{Settings: routedArgs(transport, tc.resourceGroup)})

		if err := tc.lister.List(); err != nil {
			t.Errorf("%s, resource group %q: %v", tc.service, tc.resourceGroup, err)
			continue
		}

		if !reflect.DeepEqual(transport.paths, tc.wantPaths) {
			t.Errorf("%s, resource group %q: requested %v, want %v", tc.service, tc.resourceGroup, transport.paths, tc.wantPaths)
		}
		var types []string
		for _, r := range tc.lister.Listed() {
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
	transport := &routedTransport{}
	g := &ResourceGroupLister{}
	g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, "missing")})

	if err := g.List(); err == nil {
		t.Errorf("listed %v, want an error", g.Resources)
	}
}

// A subnet's links to a route table, a security group and a NAT gateway
// become association resources imported by the subnet's ID; a subnet
// without links, or with a link that has no ID, gets none; a subnet without
// a name is skipped. Service endpoint policies follow the subnets.
func TestSubnetListsAssociationsAndPolicies(t *testing.T) {
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
	transport := &routedTransport{bodies: map[string]string{
		rg + "virtualNetworks": armList(armItem(network, "net1", "")),
		network + "/subnets": armList(
			armItem(plain, "plain", `{"addressPrefix":"10.0.0.0/24"}`),
			armItem(app, "app", `{"routeTable":{"id":"`+routeTable+`"},"networkSecurityGroup":{"id":"`+nsg+`"},"natGateway":{"id":"`+nat+`"}}`),
			armItem(bare, "bare", `{"routeTable":{}}`),
			`{"id":"`+network+`/subnets/nameless"}`,
		),
		rg + "serviceEndpointPolicies": armList(armItem(storage, "storage", "")),
	}}
	g := &SubnetLister{}
	g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, "rg1")})

	if err := g.List(); err != nil {
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
	if got := listedResources(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Each security group's rules are listed under the group's own resource
// group and labelled "<group>_<rule>", so rules of the same name in two
// groups stay apart.
func TestNetworkSecurityGroupListsRules(t *testing.T) {
	const (
		groupA = "/subscriptions/sub/resourceGroups/rg-a/providers/Microsoft.Network/networkSecurityGroups/web"
		groupB = "/subscriptions/sub/resourceGroups/rg-b/providers/Microsoft.Network/networkSecurityGroups/db"
	)
	transport := &routedTransport{bodies: map[string]string{
		"/subscriptions/sub/providers/Microsoft.Network/networkSecurityGroups": armList(
			armItem(groupA, "web", ""),
			armItem(groupB, "db", "")),
		groupA + "/securityRules": armList(
			armItem(groupA+"/securityRules/https", "https", `{"priority":100}`),
			armItem(groupA+"/securityRules/ssh", "ssh", `{"priority":200}`)),
		groupB + "/securityRules": armList(armItem(groupB+"/securityRules/https", "https", "")),
	}}
	g := &NetworkSecurityGroupLister{}
	g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, "")})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	want := []listedResource{
		{Type: "azurerm_network_security_group", ID: groupA, Name: "web"},
		{Type: "azurerm_network_security_rule", ID: groupA + "/securityRules/https", Name: "web_https"},
		{Type: "azurerm_network_security_rule", ID: groupA + "/securityRules/ssh", Name: "web_ssh"},
		{Type: "azurerm_network_security_group", ID: groupB, Name: "db"},
		{Type: "azurerm_network_security_rule", ID: groupB + "/securityRules/https", Name: "db_https"},
	}
	if got := listedResources(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A rule listing that fails fails the whole service.
func TestNetworkSecurityGroupFailsWhenRulesFail(t *testing.T) {
	const group = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/networkSecurityGroups/web"
	transport := &routedTransport{bodies: map[string]string{
		"/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/networkSecurityGroups": armList(armItem(group, "web", "")),
	}}
	g := &NetworkSecurityGroupLister{}
	g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, "rg1")})

	if err := g.List(); err == nil {
		t.Errorf("listed %v, want an error", g.Resources)
	}
}

// The Resource Manager listers record the tags their list responses carry,
// tags.<key>, as resource_graph records Resource Graph's, so that the
// provider needs no second read of them; a tag without a value is left
// out, and an untagged resource has no attributes.
func TestResourceManagerListersRecordTags(t *testing.T) {
	const (
		group   = "/subscriptions/sub/resourceGroups/rg1"
		network = group + "/providers/Microsoft.Network/"
	)
	for _, tc := range []struct {
		name   string
		lister harvest.Lister
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
		transport := &fakeTransport{body: tc.body}
		tc.lister.Configure(harvest.ListerConfig{Settings: fakeArgs(transport, "rg1")})
		if err := tc.lister.List(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := listedResources(tc.lister.Listed()); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Rules and subnets that come inline in the parent's list are used as they
// are: no list is asked for per security group or per network, and an
// inline empty list is an answer too. Only a parent without them is asked.
func TestNetworkListersUseInlineChildren(t *testing.T) {
	const (
		rg      = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Network/"
		web     = rg + "networkSecurityGroups/web"
		empty   = rg + "networkSecurityGroups/empty"
		bare    = rg + "networkSecurityGroups/bare"
		network = rg + "virtualNetworks/net1"
		app     = network + "/subnets/app"
		nsg     = rg + "networkSecurityGroups/web"
	)
	groups := &routedTransport{bodies: map[string]string{
		rg + "networkSecurityGroups": armList(
			armItem(web, "web", `{"securityRules":[`+armItem(web+"/securityRules/https", "https", `{"priority":100}`)+`],"defaultSecurityRules":[`+armItem(web+"/defaultSecurityRules/AllowVnetInBound", "AllowVnetInBound", "")+`]}`),
			armItem(empty, "empty", `{"securityRules":[]}`),
			armItem(bare, "bare", ""),
		),
		bare + "/securityRules": armList(armItem(bare+"/securityRules/ssh", "ssh", "")),
	}}
	g := &NetworkSecurityGroupLister{}
	g.Configure(harvest.ListerConfig{Settings: routedArgs(groups, "rg1")})
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	if want := []string{rg + "networkSecurityGroups", bare + "/securityRules"}; !reflect.DeepEqual(groups.paths, want) {
		t.Errorf("security groups: requested %v, want %v", groups.paths, want)
	}
	wantGroups := []listedResource{
		{Type: "azurerm_network_security_group", ID: web, Name: "web"},
		{Type: "azurerm_network_security_rule", ID: web + "/securityRules/https", Name: "web_https"},
		{Type: "azurerm_network_security_group", ID: empty, Name: "empty"},
		{Type: "azurerm_network_security_group", ID: bare, Name: "bare"},
		{Type: "azurerm_network_security_rule", ID: bare + "/securityRules/ssh", Name: "bare_ssh"},
	}
	if got := listedResources(g.Resources); !reflect.DeepEqual(got, wantGroups) {
		t.Errorf("security groups: got %+v\nwant %+v", got, wantGroups)
	}

	networks := &routedTransport{bodies: map[string]string{
		rg + "virtualNetworks": armList(armItem(network, "net1",
			`{"subnets":[`+armItem(app, "app", `{"networkSecurityGroup":{"id":"`+nsg+`"}}`)+`]}`)),
		rg + "serviceEndpointPolicies": armList(),
	}}
	s := &SubnetLister{}
	s.Configure(harvest.ListerConfig{Settings: routedArgs(networks, "rg1")})
	if err := s.List(); err != nil {
		t.Fatal(err)
	}
	if want := []string{rg + "virtualNetworks", rg + "serviceEndpointPolicies"}; !reflect.DeepEqual(networks.paths, want) {
		t.Errorf("subnets: requested %v, want %v", networks.paths, want)
	}
	wantSubnets := []listedResource{
		{Type: "azurerm_subnet", ID: app, Name: "app"},
		{
			Type: "azurerm_subnet_network_security_group_association", ID: app, Name: "app_network_security_group_association",
			Attributes: map[string]string{"subnet_id": app, "network_security_group_id": nsg},
		},
	}
	if got := listedResources(s.Resources); !reflect.DeepEqual(got, wantSubnets) {
		t.Errorf("subnets: got %+v\nwant %+v", got, wantSubnets)
	}
}

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

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The subscription the role assignment tests list, and its scope.
const (
	roleSubscriptionID = "00000000-0000-0000-0000-000000000001"
	roleSubscription   = "/subscriptions/" + roleSubscriptionID
)

// graphTransport answers each Resource Graph query with authorization for
// one of the authorizationresources table, and with groups for any
// other, and records the queries sent in order.
type graphTransport struct {
	authorization, groups string
	sent                  []string
}

func (f *graphTransport) Do(r *http.Request) (*http.Response, error) {
	sent, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	f.sent = append(f.sent, string(sent))
	body := f.groups
	if strings.Contains(string(sent), "authorizationresources") {
		body = f.authorization
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// graphRows is the JSON of one page of Resource Graph rows.
func graphRows(rows ...string) string {
	return `{"resultTruncated":"false","data":[` + strings.Join(rows, ",") + `]}`
}

// roleRow is the JSON of a role definition's row.
func roleRow(id, name, roleType string, assignableScopes ...string) string {
	return `{"id":"` + id + `","type":"microsoft.authorization/roledefinitions","roleName":"` + name + `","roleType":"` + roleType +
		`","assignableScopes":["` + strings.Join(assignableScopes, `","`) + `"]}`
}

// assignmentRow is the JSON of a role assignment's row.
func assignmentRow(id, scope, role, principalType string) string {
	return `{"id":"` + id + `","type":"microsoft.authorization/roleassignments","scope":"` + scope +
		`","roleDefinitionId":"` + role + `","principalType":"` + principalType + `"}`
}

// The role definitions the tests' assignments have: Reader, built in; a
// custom role that can be assigned in the subscription alone; and one
// that can be assigned in another subscription too.
const (
	readerRole   = roleSubscription + roleDefinitionsSegment + "acdd72a7-3385-48ef-bd42-f606fba81ae7"
	deployerRole = roleSubscription + roleDefinitionsSegment + "bbbbbbbb-0000-0000-0000-000000000001"
	sharedRole   = roleSubscription + roleDefinitionsSegment + "cccccccc-0000-0000-0000-000000000001"
	unknownRole  = roleSubscription + roleDefinitionsSegment + "dddddddd-0000-0000-0000-000000000001"
)

// roleAuthorization is the authorizationresources page of the tests: the
// role definitions above, an assignment on a resource group whose ID
// Resource Graph returns in lower case, one on the subscription, one
// inherited from a management group, one in an AKS cluster's node
// resource group, and one on a storage account, with a role Resource
// Graph didn't return.
var roleAuthorization = graphRows(
	roleRow("/providers/Microsoft.Authorization/RoleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7", "Reader", "BuiltInRole", "/"),
	roleRow(deployerRole, "App Deployer", "CustomRole", roleSubscription),
	roleRow(sharedRole, "Shared Operator", "CustomRole", roleSubscription, "/subscriptions/00000000-0000-0000-0000-000000000002"),
	assignmentRow(strings.ToLower(roleSubscription+"/resourceGroups/app"+roleAssignmentsSegment)+"11111111-1111-1111-1111-111111111111",
		roleSubscription+"/resourceGroups/app", readerRole, "ServicePrincipal"),
	assignmentRow(roleSubscription+roleAssignmentsSegment+"22222222-2222-2222-2222-222222222222", roleSubscription, deployerRole, "Group"),
	assignmentRow("/providers/Microsoft.Management/managementGroups/platform"+roleAssignmentsSegment+"44444444-4444-4444-4444-444444444444",
		"/providers/Microsoft.Management/managementGroups/platform", readerRole, "Group"),
	assignmentRow(roleSubscription+"/resourceGroups/MC_rg-aks_aks_uksouth"+roleAssignmentsSegment+"55555555-5555-5555-5555-555555555555",
		roleSubscription+"/resourceGroups/MC_rg-aks_aks_uksouth", sharedRole, "ServicePrincipal"),
	assignmentRow(roleSubscription+"/resourceGroups/data/providers/Microsoft.Storage/storageAccounts/stdata"+roleAssignmentsSegment+"66666666-6666-6666-6666-666666666666",
		roleSubscription+"/resourceGroups/data/providers/Microsoft.Storage/storageAccounts/stdata", unknownRole, "User"),
	`{"type":"microsoft.authorization/roleassignments"}`,
)

// aksCluster manages the node resource group of roleGroups.
const aksCluster = roleSubscription + "/resourceGroups/rg-aks/providers/Microsoft.ContainerService/managedClusters/aks"

// roleGroups are the subscription's resource groups, as
// graphResourceGroups lists them.
var roleGroups = graphRows(
	`{"id":"`+roleSubscription+`/resourceGroups/app","tags":{},"managedBy":""}`,
	`{"id":"`+roleSubscription+`/resourceGroups/data","tags":{},"managedBy":""}`,
	`{"id":"`+roleSubscription+`/resourceGroups/MC_rg-aks_aks_uksouth","tags":{},"managedBy":"`+aksCluster+`"}`,
)

// roleArgs are the arguments Select hands role_assignment for the tests'
// subscription and resourceGroup, with every request answered by
// transport.
func roleArgs(transport *graphTransport, resourceGroup string) map[string]interface{} {
	return map[string]interface{}{
		"subscription_id": roleSubscriptionID,
		"resource_group":  resourceGroup,
		"credential":      azcore.TokenCredential(fakeCredential{}),
		"client_options":  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
	}
}

// The subscription's assignments and the custom roles only it can assign
// come from one query of authorizationresources, and the resource groups
// from another, for what manages them. Import IDs are as azurerm parses
// them, whatever case Resource Graph returns them in; an assignment is
// named after its scope and its role.
func TestRoleAssignmentListsTheSubscription(t *testing.T) {
	transport := &graphTransport{authorization: roleAuthorization, groups: roleGroups}
	g := &RoleAssignmentLister{}
	g.Configure(harvest.ListerConfig{Settings: roleArgs(transport, "")})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	if len(transport.sent) != 2 || !strings.Contains(transport.sent[0], "authorizationresources") || !strings.Contains(transport.sent[1], "resourcecontainers") {
		t.Errorf("queries: %v, want authorizationresources, then the resource groups", transport.sent)
	}
	want := []listedResource{
		{Type: "azurerm_role_definition", ID: deployerRole + "|" + roleSubscription, Name: "App Deployer", Attributes: map[string]string{"name": "App Deployer"}},
		{Type: "azurerm_role_assignment", ID: roleSubscription + "/resourceGroups/app" + roleAssignmentsSegment + "11111111-1111-1111-1111-111111111111", Name: "app Reader", Attributes: map[string]string{
			"name": "11111111-1111-1111-1111-111111111111", "scope": roleSubscription + "/resourceGroups/app", "principal_type": "ServicePrincipal", "resource_group_name": "app",
		}},
		{Type: "azurerm_role_assignment", ID: roleSubscription + roleAssignmentsSegment + "22222222-2222-2222-2222-222222222222", Name: "subscription App Deployer", Attributes: map[string]string{
			"name": "22222222-2222-2222-2222-222222222222", "scope": roleSubscription, "principal_type": "Group", "role_definition_listed": "true",
		}},
		{Type: "azurerm_role_assignment", ID: "/providers/Microsoft.Management/managementGroups/platform" + roleAssignmentsSegment + "44444444-4444-4444-4444-444444444444", Name: "platform Reader", Attributes: map[string]string{
			"name": "44444444-4444-4444-4444-444444444444", "scope": "/providers/Microsoft.Management/managementGroups/platform", "principal_type": "Group", "inherited": "true",
		}},
		{Type: "azurerm_role_assignment", ID: roleSubscription + "/resourceGroups/MC_rg-aks_aks_uksouth" + roleAssignmentsSegment + "55555555-5555-5555-5555-555555555555", Name: "MC_rg-aks_aks_uksouth Shared Operator", Attributes: map[string]string{
			"name": "55555555-5555-5555-5555-555555555555", "scope": roleSubscription + "/resourceGroups/MC_rg-aks_aks_uksouth", "principal_type": "ServicePrincipal",
			"resource_group_name": "MC_rg-aks_aks_uksouth", "resource_group_managed_by": aksCluster,
		}},
		{Type: "azurerm_role_assignment", ID: roleSubscription + "/resourceGroups/data/providers/Microsoft.Storage/storageAccounts/stdata" + roleAssignmentsSegment + "66666666-6666-6666-6666-666666666666", Name: "stdata 66666666", Attributes: map[string]string{
			"name": "66666666-6666-6666-6666-666666666666", "scope": roleSubscription + "/resourceGroups/data/providers/Microsoft.Storage/storageAccounts/stdata", "principal_type": "User", "resource_group_name": "data",
		}},
	}
	got := listedResources(g.Resources)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%+v\nwant:\n%+v", got, want)
	}

	// The catalogue leaves out the inherited assignment and the one in the
	// node resource group, names a built-in role by name and a listed
	// custom role by ID, for the reference to its definition.
	cat := catalogue.MustBuiltin("azurerm")
	skipped := map[string]bool{}
	for _, r := range g.Resources {
		if cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes) != "" {
			skipped[r.RawName] = true
		}
	}
	if want := map[string]bool{"platform Reader": true, "MC_rg-aks_aks_uksouth Shared Operator": true}; !reflect.DeepEqual(skipped, want) {
		t.Errorf("left out by default: %v, want %v", skipped, want)
	}
	for i, omit := range map[int][]string{1: {"role_definition_id"}, 2: {"role_definition_name"}} {
		r := g.Resources[i]
		if got := cat.ResourceOmit(r.Address.Type, r.Observed.Attributes); !reflect.DeepEqual(got, omit) {
			t.Errorf("%s: leaves out %v, want %v", r.RawName, got, omit)
		}
	}
}

// With --resource-group, only the assignments on the group and what is in
// it are listed, and only the group is asked about; a custom role that
// can be assigned in the whole subscription isn't the group's.
func TestRoleAssignmentHonoursTheResourceGroup(t *testing.T) {
	transport := &graphTransport{authorization: roleAuthorization, groups: roleGroups}
	g := &RoleAssignmentLister{}
	g.Configure(harvest.ListerConfig{Settings: roleArgs(transport, "app")})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	if len(transport.sent) != 2 || !strings.Contains(transport.sent[1], "resourceGroup =~ 'app'") {
		t.Errorf("queries: %v, want the resource group app's", transport.sent)
	}
	got := listedResources(g.Resources)
	if len(got) != 1 || got[0].Name != "app Reader" {
		t.Errorf("got %+v, want the assignment on app alone", got)
	}
}

// Without an assignment in a resource group, the resource groups aren't
// asked for.
func TestRoleAssignmentAsksForGroupsOnlyWhenNeeded(t *testing.T) {
	transport := &graphTransport{authorization: graphRows(
		assignmentRow(roleSubscription+roleAssignmentsSegment+"22222222-2222-2222-2222-222222222222", roleSubscription, readerRole, "User"),
	)}
	g := &RoleAssignmentLister{}
	g.Configure(harvest.ListerConfig{Settings: roleArgs(transport, "")})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	if len(transport.sent) != 1 {
		t.Errorf("queries: %v, want authorizationresources alone", transport.sent)
	}
	// Resource Graph returned no Reader role: the name starts with the
	// assignment's.
	if got := listedResources(g.Resources); len(got) != 1 || got[0].Name != "subscription 22222222" {
		t.Errorf("got %+v", got)
	}
}

// A subscription ID that isn't a GUID would go into the query: it fails
// before any request.
func TestRoleAssignmentRejectsAMalformedSubscription(t *testing.T) {
	transport := &graphTransport{}
	args := roleArgs(transport, "")
	args["subscription_id"] = "sub' or 1==1"
	g := &RoleAssignmentLister{}
	g.Configure(harvest.ListerConfig{Settings: args})
	if err := g.List(); err == nil || len(transport.sent) != 0 {
		t.Errorf("err %v, queries %v", err, transport.sent)
	}
}

func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		scope, base string
		want        bool
	}{
		{roleSubscription, roleSubscription, true},
		{strings.ToUpper(roleSubscription) + "/resourceGroups/app", roleSubscription, true},
		{roleSubscription + "2", roleSubscription, false},
		{"/", roleSubscription, false},
		{"/providers/Microsoft.Management/managementGroups/platform", roleSubscription, false},
		{roleSubscription + "/resourceGroups/app-2", roleSubscription + "/resourceGroups/app", false},
	} {
		if got := within(tc.scope, tc.base); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.scope, tc.base, got, tc.want)
		}
	}
}

func TestAssignmentScope(t *testing.T) {
	for id, want := range map[string][2]string{
		roleSubscription + roleAssignmentsSegment + "a":                           {roleSubscription, "a"},
		strings.ToLower(roleAssignmentsSegment) + "a":                             {"/", "a"},
		roleSubscription + "/resourceGroups/app" + roleAssignmentsSegment + "a/x": {"", ""},
		roleSubscription + "/resourceGroups/app":                                  {"", ""},
	} {
		scope, name, _ := assignmentScope(id)
		if [2]string{scope, name} != want {
			t.Errorf("%s: got %q, %q, want %q", id, scope, name, want)
		}
	}
}

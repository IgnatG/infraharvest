// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The Resource Graph types of a role assignment and a role definition, as
// the authorizationresources table has them.
const (
	roleAssignmentGraphType = "microsoft.authorization/roleassignments"
	roleDefinitionGraphType = "microsoft.authorization/roledefinitions"
)

// authorizationQuery lists the role assignments and role definitions
// Resource Graph has for the subscription it is asked about, with only
// what RoleAssignmentLister reads of them: not a definition's
// permissions, which Terraform reads itself.
const authorizationQuery = "authorizationresources" +
	" | where type in~ ('" + roleAssignmentGraphType + "', '" + roleDefinitionGraphType + "')" +
	" | extend scope = tostring(properties.scope), roleDefinitionId = tostring(properties.roleDefinitionId)," +
	" principalType = tostring(properties.principalType), roleName = tostring(properties.roleName)," +
	" roleType = tostring(properties.type), assignableScopes = properties.assignableScopes" +
	" | project id, type, scope, roleDefinitionId, principalType, roleName, roleType, assignableScopes" +
	" | order by id asc"

// The import IDs' segments of a role assignment and a role definition, as
// azurerm parses them, whatever case Resource Graph returns them in.
const (
	roleAssignmentsSegment = "/providers/Microsoft.Authorization/roleAssignments/"
	roleDefinitionsSegment = "/providers/Microsoft.Authorization/roleDefinitions/"
)

// RoleAssignmentLister lists the Azure RBAC role assignments of the
// subscription, or of --resource-group and what is in it, as
// azurerm_role_assignment, and the custom roles that can be assigned only
// there as azurerm_role_definition, from one Resource Graph query of the
// authorizationresources table, which Resource Manager would take a list
// per scope for. Role assignments are extension resources, which the
// resources table resource_graph lists doesn't have.
//
// An assignment records its name, its scope's resource group and what
// manages that group (see graphResourceGroups, asked only when an
// assignment is in a group), so that the catalogue's rules for every type
// leave out the assignments in the groups they leave out, such as those
// AKS makes in its MC_* group; whether it is inherited, as from a
// management group, the tenant root or another subscription, which the
// catalogue leaves out too; its principal's type; and whether its role is
// a custom role the service lists, which the assignment then names by ID,
// for a reference to the azurerm_role_definition, and otherwise by name,
// as a built-in role is written by hand. Nothing in an assignment tells
// one Azure made from one a user made: those Azure makes, such as AKS's
// for its identities, are imported where their scope is.
type RoleAssignmentLister struct {
	Service
}

// authorizationRow is what RoleAssignmentLister reads of a row of
// authorizationQuery.
type authorizationRow struct {
	id, typ                                string
	scope, roleDefinitionID, principalType string
	roleName, roleType                     string
	assignableScopes                       []string
}

// List lists the assignments and custom roles of the run's scope: the
// subscription, or --resource-group and what is in it.
func (az *RoleAssignmentLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	predicate, err := graphPredicate(subscriptionID, resourceGroup)
	if err != nil {
		return err
	}
	rows, err := authorizationRows(az.Context(), credential, options, subscriptionID)
	if err != nil {
		return err
	}
	subscription := "/subscriptions/" + subscriptionID
	run := subscription
	if resourceGroup != "" {
		run += "/resourceGroups/" + resourceGroup
	}
	roles := map[string]authorizationRow{}
	listed := map[string]bool{}
	for _, row := range rows {
		if !strings.EqualFold(row.typ, roleDefinitionGraphType) {
			continue
		}
		guid := strings.ToLower(lastSegment(row.id))
		roles[guid] = row
		if !strings.EqualFold(row.roleType, "CustomRole") || len(row.assignableScopes) == 0 {
			continue
		}
		// A custom role that can be assigned elsewhere too, as in another
		// subscription, is shared: assignments name it, and what manages
		// those scopes manages it.
		inRun := true
		for _, scope := range row.assignableScopes {
			if !within(scope, run) {
				inRun = false
			}
		}
		if inRun {
			listed[guid] = true
			id := subscription + roleDefinitionsSegment + guid + "|" + subscription
			az.Resources = append(az.Resources, harvest.Record(id, row.roleName, "azurerm_role_definition", az.Provider(), map[string]string{"name": row.roleName}))
		}
	}
	var assignments []harvest.Resource
	inGroups := false
	for _, row := range rows {
		if !strings.EqualFold(row.typ, roleAssignmentGraphType) {
			continue
		}
		prefix, name, ok := assignmentScope(row.id)
		if !ok {
			log.Printf("azurerm: role_assignment: %s isn't a role assignment's ID; left out", row.id)
			continue
		}
		scope := prefix
		if strings.EqualFold(row.scope, prefix) {
			scope = row.scope
		}
		// One assigned above the subscription is recorded, for the
		// catalogue to leave out with the reason; with --resource-group,
		// one outside the group isn't listed, as other services list
		// nothing outside it.
		inherited := !within(scope, run)
		if inherited && (resourceGroup != "" || within(scope, subscription)) {
			continue
		}
		role := roles[strings.ToLower(lastSegment(row.roleDefinitionID))]
		attributes := map[string]string{"name": name, "scope": scope}
		if row.principalType != "" {
			attributes["principal_type"] = row.principalType
		}
		if listed[strings.ToLower(lastSegment(row.roleDefinitionID))] {
			attributes["role_definition_listed"] = "true"
		}
		if inherited {
			attributes["inherited"] = "true"
		} else if rg := scopeResourceGroup(scope); rg != "" {
			attributes["resource_group_name"] = rg
			inGroups = true
		}
		id := strings.TrimSuffix(scope, "/") + roleAssignmentsSegment + name
		assignments = append(assignments, harvest.Record(id, assignmentName(scope, subscription, role.roleName, name), "azurerm_role_assignment", az.Provider(), attributes))
	}
	if inGroups {
		clientOptions := arm.ClientOptions{}
		if options != nil {
			clientOptions = *options
		}
		groups, err := graphResourceGroups(az.Context(), credential, &clientOptions, subscriptionID, predicate)
		if err != nil {
			return err
		}
		withGroupManagers(assignments, subscription, managedResourceGroups(groups))
	}
	if az.Verbose() {
		log.Printf("azurerm: role_assignment: %d role assignments and %d custom roles from %d rows of Resource Graph", len(assignments), len(az.Resources), len(rows))
	}
	az.Resources = append(az.Resources, assignments...)
	return nil
}

// authorizationRows returns the rows of authorizationQuery for the
// subscription, every page of them. A row without an ID is left out.
func authorizationRows(ctx context.Context, credential azcore.TokenCredential, options *arm.ClientOptions, subscriptionID string) ([]authorizationRow, error) {
	data, err := graphQuery(ctx, credential, options, armresourcegraph.QueryRequest{
		Query:         to.Ptr(authorizationQuery),
		Subscriptions: []*string{to.Ptr(subscriptionID)},
	})
	if err != nil {
		return nil, fmt.Errorf("role assignments from Resource Graph: %w", err)
	}
	var rows []authorizationRow
	for _, m := range data {
		row := authorizationRow{
			id:               stringOf(m, "id"),
			typ:              stringOf(m, "type"),
			scope:            stringOf(m, "scope"),
			roleDefinitionID: stringOf(m, "roleDefinitionId"),
			principalType:    stringOf(m, "principalType"),
			roleName:         stringOf(m, "roleName"),
			roleType:         stringOf(m, "roleType"),
		}
		scopes, _ := m["assignableScopes"].([]interface{})
		for _, s := range scopes {
			if s, ok := s.(string); ok && s != "" {
				row.assignableScopes = append(row.assignableScopes, s)
			}
		}
		if row.id != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// stringOf returns the string at key in m, or "".
func stringOf(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// within reports whether scope is base or under it, whatever the case, as
// Azure compares IDs. The root scope, /, is under nothing else.
func within(scope, base string) bool {
	scope, base = strings.ToLower(strings.TrimSuffix(scope, "/")), strings.ToLower(strings.TrimSuffix(base, "/"))
	return scope != "" && (scope == base || strings.HasPrefix(scope, base+"/"))
}

// assignmentScope splits a role assignment's ID into its scope, "/" for
// the root, and its name, whatever the case of the
// /providers/Microsoft.Authorization/roleAssignments/ between them.
func assignmentScope(id string) (scope, name string, ok bool) {
	i := strings.LastIndex(strings.ToLower(id), strings.ToLower(roleAssignmentsSegment))
	if i < 0 {
		return "", "", false
	}
	scope, name = id[:i], id[i+len(roleAssignmentsSegment):]
	if name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	if scope == "" {
		scope = "/"
	}
	return scope, name, true
}

// scopeResourceGroup returns the resource group a scope is, or is in, or
// "" for one that isn't in a resource group.
func scopeResourceGroup(scope string) string {
	segments := strings.Split(scope, "/")
	for i := 0; i+1 < len(segments); i++ {
		if strings.EqualFold(segments[i], "resourceGroups") && segments[i+1] != "" {
			return segments[i+1]
		}
	}
	return ""
}

// assignmentName is what an assignment is called in the configuration:
// what it is on (the subscription, or the last name of its scope) and its
// role, such as app Storage Blob Data Reader; with no role name, as for a
// role Resource Graph didn't return, the start of its name.
func assignmentName(scope, subscription, roleName, name string) string {
	on := lastSegment(strings.TrimSuffix(scope, "/"))
	switch {
	case scope == "/":
		on = "root"
	case strings.EqualFold(strings.TrimSuffix(scope, "/"), subscription):
		on = "subscription"
	}
	if roleName == "" {
		roleName, _, _ = strings.Cut(name, "-")
	}
	return on + " " + roleName
}

// withGroupManagers records, of each resource of the subscription whose
// resource group another resource manages (managed, see
// managedResourceGroups), what manages the group, as
// resource_group_managed_by.
func withGroupManagers(resources []harvest.Resource, subscription string, managed map[string]string) {
	for _, r := range resources {
		rg := r.Observed.Attributes["resource_group_name"]
		if rg == "" {
			continue
		}
		if by := managed[strings.ToUpper(subscription+"/resourceGroups/"+rg)]; by != "" {
			r.Observed.Attributes["resource_group_managed_by"] = by
		}
	}
}

// PartialTypes names the types a listing of the run's scope can leave out
// while they exist: a custom role is listed only while every scope it
// can be assigned at is in the run (see RoleAssignmentLister), which a
// change to the role can undo without deleting it, so a custom role the
// listing didn't find isn't taken for one deleted.
func (*Provider) PartialTypes() []string {
	return []string{"azurerm_role_definition"}
}

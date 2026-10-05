// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package keycloak

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/mrparkers/terraform-provider-keycloak/keycloak"
)

type RealmGenerator struct {
	KeycloakService
}

func (g *RealmGenerator) InitResources() error {
	var realms []*keycloak.Realm
	var realmsGroups []*keycloak.Group
	ctx := context.TODO()

	// Connect to keycloak instance
	userAgent := "infraharvest (+https://github.com/IgnatG/infraharvest) Terraform Plugin SDK/2.10.1"
	kck, err := keycloak.NewKeycloakClient(ctx, g.GetArgs()["url"].(string), g.GetArgs()["base_path"].(string), g.GetArgs()["client_id"].(string), g.GetArgs()["client_secret"].(string), g.GetArgs()["realm"].(string), "", "", true, g.GetArgs()["client_timeout"].(int), g.GetArgs()["root_ca_certificate"].(string), g.GetArgs()["tls_insecure_skip_verify"].(bool), userAgent, g.GetArgs()["red_hat_sso"].(bool), make(map[string]string))
	if err != nil {
		return errors.New("keycloak: could not connect to Keycloak")
	}

	// Get realm resources
	target := g.GetArgs()["target"].(string)
	if target == "" {
		realms, err = kck.GetRealms(ctx)
		if err != nil {
			return errors.New("keycloak: could not get realms attributes in Keycloak")
		}
	} else {
		realm, err := kck.GetRealm(ctx, target)
		if err != nil {
			return errors.New("keycloak: could not get " + target + " realm attributes in Keycloak")
		}
		realms = append(realms, realm)
	}
	g.Resources = append(g.Resources, g.createRealmResources(realms)...)

	// For each realm, get resources
	for _, realm := range realms {
		// Get required actions resources
		requiredActions, err := kck.GetRequiredActions(ctx, realm.Realm)
		if err != nil {
			return fmt.Errorf("keycloak: could not get required actions of realm %s in Keycloak. err: %w", realm.Realm, err)
		}
		g.Resources = append(g.Resources, g.createRequiredActionResources(requiredActions)...)

		// Get top-level authentication flows resources
		authenticationFlows, err := kck.ListAuthenticationFlows(ctx, realm.Realm)
		if err != nil {
			return fmt.Errorf("keycloak: could not get authentication flows of realm %s in Keycloak. err: %w", realm.Realm, err)
		}
		g.Resources = append(g.Resources, g.createAuthenticationFlowResources(authenticationFlows)...)

		// For each authentication flow, get subFlow, execution and execution config resources
		for _, topLevelAuthenticationFlow := range authenticationFlows {
			authenticationSubFlowOrExecutions, err := kck.ListAuthenticationExecutions(ctx, realm.Realm, topLevelAuthenticationFlow.Alias)
			if err != nil {
				return fmt.Errorf("keycloak: could not get authentication executions of authentication flow %s of realm %s in Keycloak. err: %w",
					topLevelAuthenticationFlow.Alias, realm.Realm, err)
			}

			var stack []*keycloak.AuthenticationExecutionInfo
			parentFlowAlias := topLevelAuthenticationFlow.Alias

			for _, authenticationSubFlowOrExecution := range authenticationSubFlowOrExecutions {

				// Find the parent flow alias
				if len(stack) > 0 {
					previous := stack[len(stack)-1]
					if authenticationSubFlowOrExecution.Level < previous.Level {
						// Find the last sub flow/execution for the current level
						stack = stack[:authenticationSubFlowOrExecution.Level+1]
						previous = stack[len(stack)-1]
					}
					if authenticationSubFlowOrExecution.Level == previous.Level {
						// Same level sub flow/execution, it means that the sub flow/execution has same parent flow of the last sub flow/execution
						parentFlowAlias = previous.ParentFlowAlias

					} else if authenticationSubFlowOrExecution.Level > previous.Level {
						// Deep level sub flow/execution, it means that the parent flow is the last sub flow/execution
						if previous.AuthenticationFlow {
							parentFlowAlias = previous.Alias
						} else {
							return errors.New("keycloak: invalid parent sub flow, it should be a sub flow but it's an execution")
						}
					}
				}

				var resource terraformutils.Resource

				switch authenticationSubFlowOrExecution.AuthenticationFlow {
				case true:
					authenticationSubFlow, err := kck.GetAuthenticationSubFlow(ctx, realm.Realm, parentFlowAlias, authenticationSubFlowOrExecution.FlowId)
					if err != nil {
						return fmt.Errorf("keycloak: could not get authentication subflow %s of realm %s in Keycloak. err: %w",
							authenticationSubFlowOrExecution.FlowId, realm.Realm, err)
					}

					// Need to store the alias and parent flow alias
					authenticationSubFlowOrExecution.Alias = authenticationSubFlow.Alias
					authenticationSubFlowOrExecution.ParentFlowAlias = parentFlowAlias

					resource = g.createAuthenticationSubFlowResource(authenticationSubFlow)
					g.Resources = append(g.Resources, resource)

				case false:
					authenticationExecution, err := kck.GetAuthenticationExecution(ctx, realm.Realm, parentFlowAlias, authenticationSubFlowOrExecution.Id)
					if err != nil {
						return fmt.Errorf("keycloak: could not get authentication execution %s of realm %s in Keycloak. err: %w",
							authenticationSubFlowOrExecution.Id, realm.Realm, err)
					}

					// Need to store the parent flow alias
					authenticationSubFlowOrExecution.ParentFlowAlias = parentFlowAlias

					resource = g.createAuthenticationExecutionResource(authenticationExecution)
					g.Resources = append(g.Resources, resource)

					if authenticationSubFlowOrExecution.AuthenticationConfig != "" {
						authenticationExecutionConfig := &keycloak.AuthenticationExecutionConfig{
							RealmId:     realm.Realm,
							Id:          authenticationSubFlowOrExecution.AuthenticationConfig,
							ExecutionId: authenticationSubFlowOrExecution.Id,
						}
						err := kck.GetAuthenticationExecutionConfig(ctx, authenticationExecutionConfig)
						if err != nil {
							return fmt.Errorf("keycloak: could not get authentication execution config %s of realm %s in Keycloak. err: %w",
								authenticationExecutionConfig.Id, realm.Realm, err)
						}

						g.Resources = append(g.Resources, g.createAuthenticationExecutionConfigResource(authenticationExecutionConfig))
					}
				}

				// Stack the current sub flow/execution
				if len(stack) > 0 && stack[len(stack)-1].Level == authenticationSubFlowOrExecution.Level {
					// Replace it if it's same level
					stack[len(stack)-1] = authenticationSubFlowOrExecution
				} else {
					stack = append(stack, authenticationSubFlowOrExecution)
				}
			}
		}

		// Get custom federations resources
		// TODO: support kerberos user federation
		customUserFederations, err := kck.GetCustomUserFederations(ctx, realm.Realm, realm.Id)
		if err != nil {
			return errors.New("keycloak: could not get custom user federations of realm " + realm.Id + " in Keycloak")
		}
		g.Resources = append(g.Resources, g.createCustomUserFederationResources(customUserFederations)...)

		// For each custom federation, get mappers resources
		for _, customUserFederation := range *customUserFederations {
			if customUserFederation.ProviderId == "ldap" {
				mappers, err := kck.GetLdapUserFederationMappers(ctx, realm.Realm, customUserFederation.Id)
				if err != nil {
					return errors.New("keycloak: could not get mappers of ldap user federation " + customUserFederation.Name + " of realm " + realm.Realm + " in Keycloak")
				}
				g.Resources = append(g.Resources, g.createLdapMapperResources(realm.Realm, customUserFederation.Name, mappers)...)
			}
		}

		// Get groups tree and default groups resources
		realmGroups, err := kck.GetGroups(ctx, realm.Realm)
		if err != nil {
			return errors.New("keycloak: could not get groups of realm " + realm.Realm + " in Keycloak")
		}
		realmsGroups = append(realmsGroups, realmGroups...)
		g.Resources = append(g.Resources, g.createDefaultGroupResource(realm.Realm))

		// Get users resources
		realmUsers, err := kck.GetUsers(ctx, realm.Realm)
		if err != nil {
			return errors.New("keycloak: could not get users of realm " + realm.Realm + " in Keycloak")
		}
		g.Resources = append(g.Resources, g.createUserResources(realmUsers)...)

		// Get realm open id client scopes resources
		realmScopes, err := kck.ListOpenidClientScopesWithFilter(ctx, realm.Realm, func(scope *keycloak.OpenidClientScope) bool { return true })
		if err != nil {
			return errors.New("keycloak: could not get realm scopes of realm " + realm.Realm + " in Keycloak")
		}
		g.Resources = append(g.Resources, g.createScopeResources(realm.Realm, realmScopes)...)

		// Get open id clients
		realmClients, err := kck.GetOpenidClients(ctx, realm.Realm, true)
		if err != nil {
			return errors.New("keycloak: could not get open id clients of realm " + realm.Realm + " in Keycloak")
		}
		g.Resources = append(g.Resources, g.createOpenIDClientResources(realmClients)...)

		// For each open id client, get resources
		mapServiceAccountIds := map[string]map[string]string{}
		mapContainerIDs := map[string]string{}
		mapClientIDs := map[string]string{}
		for _, client := range realmClients {
			mapClientIDs[client.Id] = client.ClientId
			mapContainerIDs[client.Id] = "_" + client.ClientId

			// Get open id client protocol mappers resources
			clientMappers, err := kck.GetGenericProtocolMappers(ctx, realm.Realm, client.Id)
			if err != nil {
				return errors.New("keycloak: could not get protocol mappers of open id client " + client.ClientId + " of realm " + realm.Realm + " in Keycloak")
			}
			g.Resources = append(g.Resources, g.createOpenIDProtocolMapperResources(client.ClientId, clientMappers)...)

			// Get open id client default scopes resources
			clientScopes, err := kck.GetOpenidDefaultClientScopes(ctx, realm.Realm, client.Id)
			if err != nil {
				return errors.New("keycloak: could not get default client scopes of open id client " + client.ClientId + " of realm " + realm.Realm + " in Keycloak")
			}
			if len(*clientScopes) > 0 {
				g.Resources = append(g.Resources, g.createOpenidClientScopesResources(realm.Realm, client.Id, client.ClientId, "default", clientScopes))
			}

			// Get open id client optional scopes resources
			clientScopes, err = kck.GetOpenidOptionalClientScopes(ctx, realm.Realm, client.Id)
			if err != nil {
				return errors.New("keycloak: could not get optional client scopes of open id client " + client.ClientId + " of realm " + realm.Realm + " in Keycloak")
			}
			if len(*clientScopes) > 0 {
				g.Resources = append(g.Resources, g.createOpenidClientScopesResources(realm.Realm, client.Id, client.ClientId, "optional", clientScopes))
			}

			// Prepare a slice to be able to link roles associated to service account roles to be associated to the open id client, only if service accounts are enabled
			if !client.ServiceAccountsEnabled {
				continue
			}
			serviceAccountUser, err := kck.GetOpenidClientServiceAccountUserId(ctx, realm.Realm, client.Id)
			if err != nil {
				return errors.New("keycloak: could not get service account user associated to open id client " + client.ClientId + " of realm " + realm.Realm + " in Keycloak")
			}
			mapServiceAccountIds[serviceAccountUser.Id] = map[string]string{}
			mapServiceAccountIds[serviceAccountUser.Id]["Id"] = client.Id
			mapServiceAccountIds[serviceAccountUser.Id]["ClientId"] = client.ClientId
		}

		// Get open id client roles
		clientRoles, err := kck.GetClientRoles(ctx, realm.Realm, realmClients)
		if err != nil {
			return errors.New("keycloak: could not get open id clients roles of realm " + realm.Realm + " in Keycloak")
		}

		// Get roles
		realmRoles, err := kck.GetRealmRoles(ctx, realm.Realm)
		if err != nil {
			return errors.New("keycloak: could not get realm roles of realm " + realm.Realm + " in Keycloak")
		}

		// Set ContainerId of the roles, for realm = "", for open id clients = "_" + client.ClientId
		// and get roles resources
		mapContainerIDs[realm.Realm] = ""
		roles := append(clientRoles, realmRoles...)
		for _, role := range roles {
			role.ContainerId = mapContainerIDs[role.ContainerId]
		}
		g.Resources = append(g.Resources, g.createRoleResources(roles)...)

		// Get service account roles resources
		usersInRole, err := kck.GetClientRoleUsers(ctx, realm.Realm, clientRoles)
		if err != nil {
			return errors.New("keycloak: could not get users roles of realm " + realm.Realm + " in Keycloak")
		}
		g.Resources = append(g.Resources, g.createServiceAccountClientRolesResources(realm.Realm, clientRoles, *usersInRole, mapServiceAccountIds, mapClientIDs)...)
	}

	// Parse the groups trees, and get all the groups
	// Get groups resources
	groups := g.flattenGroups(realmsGroups, "")
	g.Resources = append(g.Resources, g.createGroupResources(groups)...)

	// For each group, get group memberships and roles resources
	for _, group := range groups {
		// Get group members resources
		members, err := kck.GetGroupMembers(ctx, group.RealmId, group.Id)
		if err != nil {
			return errors.New("keycloak: could not get group members of group " + group.Name + " in Keycloak")
		}
		if len(members) > 0 {
			groupMembers := make([]string, len(members))
			for k, member := range members {
				groupMembers[k] = member.Username
			}
			g.Resources = append(g.Resources, g.createGroupMembershipsResource(group.RealmId, group.Id, group.Name, groupMembers))
		}

		// Get group roles resources
		// For realm roles and open id clients roles
		groupDetails, err := kck.GetGroup(ctx, group.RealmId, group.Id)
		if err != nil {
			return errors.New("keycloak: could not get details about group " + group.Name + " in Keycloak")
		}
		groupRoles := []string{}
		if len(groupDetails.RealmRoles) > 0 {
			groupRoles = append(groupRoles, groupDetails.RealmRoles...)
		}
		if len(groupDetails.ClientRoles) > 0 {
			for _, clientRoles := range groupDetails.ClientRoles {
				groupRoles = append(groupRoles, clientRoles...)
			}
		}
		if len(groupRoles) > 0 {
			g.Resources = append(g.Resources, g.createGroupRolesResource(group.RealmId, group.Id, group.Name, groupRoles))
		}
	}

	return nil
}

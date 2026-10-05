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

package alicloud

import (
	"strings"

	"github.com/IgnatG/infraharvest/providers/alicloud/connectivity"
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ram"
)

// RAMGenerator Struct for generating AliCloud Elastic Compute Service
type RAMGenerator struct {
	AliCloudService
}

func resourceFromRAMRole(role ram.RoleInListRoles) terraformutils.Resource {
	return terraformutils.NewResource(
		role.RoleName,
		role.RoleId+"__"+role.RoleName,
		"alicloud_ram_role",
		"alicloud",
		map[string]string{})

}

func resourceFromRAMPolicy(policy ram.PolicyInListPoliciesForRole, roleName string) terraformutils.Resource {
	// https://github.com/terraform-providers/terraform-provider-alicloud/blob/master/alicloud/resource_alicloud_ram_role_policy_attachment.go#L93
	id := strings.Join([]string{"role", policy.PolicyName, policy.PolicyType, roleName}, ":")

	return terraformutils.NewResource(
		id,
		id+"__"+roleName+"_"+policy.PolicyName,
		"alicloud_ram_role_policy_attachment",
		"alicloud",
		map[string]string{})

}

func initRoles(client *connectivity.AliyunClient) ([]ram.RoleInListRoles, error) {
	allRoles := make([]ram.RoleInListRoles, 0)

	raw, err := client.WithRAMClient(func(ramClient *ram.Client) (interface{}, error) {
		request := ram.CreateListRolesRequest()
		request.RegionId = client.RegionID
		return ramClient.ListRoles(request)
	})
	if err != nil {
		return nil, err
	}

	response := raw.(*ram.ListRolesResponse)
	allRoles = append(allRoles, response.Roles.Role...)

	return allRoles, nil
}

func initRAMPolicyAttachment(client *connectivity.AliyunClient, allRoles []ram.RoleInListRoles) ([]ram.PolicyInListPoliciesForRole, []string, error) {
	allRAMPolicies := make([]ram.PolicyInListPoliciesForRole, 0)
	roleNames := make([]string, 0)

	for _, role := range allRoles {
		raw, err := client.WithRAMClient(func(ramClient *ram.Client) (interface{}, error) {
			request := ram.CreateListPoliciesForRoleRequest()
			request.RegionId = client.RegionID
			request.RoleName = role.RoleName
			return ramClient.ListPoliciesForRole(request)
		})
		if err != nil {
			return nil, nil, err
		}

		response := raw.(*ram.ListPoliciesForRoleResponse)
		for _, policy := range response.Policies.Policy {
			allRAMPolicies = append(allRAMPolicies, policy)
			roleNames = append(roleNames, role.RoleName)
		}
	}

	return allRAMPolicies, roleNames, nil
}

// InitResources Gets the list of all ram role ids and generates resources
func (g *RAMGenerator) InitResources() error {
	client, err := g.LoadClientFromProfile()
	if err != nil {
		return err
	}

	allRoles, err := initRoles(client)
	if err != nil {
		return err
	}

	allRAMPolicyAttachment, roleNames, err := initRAMPolicyAttachment(client, allRoles)
	if err != nil {
		return err
	}

	for _, role := range allRoles {
		resource := resourceFromRAMRole(role)
		g.Resources = append(g.Resources, resource)
	}

	for i, ramPolicy := range allRAMPolicyAttachment {
		resource := resourceFromRAMPolicy(ramPolicy, roleNames[i])
		g.Resources = append(g.Resources, resource)
	}

	return nil
}

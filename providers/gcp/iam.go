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

package gcp

import (
	"context"
	"log"
	"regexp"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"
	iam "google.golang.org/api/iam/v1"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type IamGenerator struct {
	GCPService
}

// serviceAccountEmail is what google_service_account accepts.
var serviceAccountEmail = regexp.MustCompile(`^[a-z]`)

func (g *IamGenerator) createServiceAccountResources(accounts []*iam.ServiceAccount) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, account := range accounts {
		if !serviceAccountEmail.MatchString(account.Email) {
			log.Printf("skipping %s: service account email must start with [a-z]\n", account.Name)
			continue
		}
		resources = append(resources, terraformutils.NewSimpleResource(
			account.Name,
			account.UniqueId,
			"google_service_account",
			g.ProviderName))
	}
	return resources
}

func (g *IamGenerator) createIamCustomRoleResources(roles []*iam.Role, project string) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, role := range roles {
		if role.Deleted {
			continue
		}
		// projects/<project>/roles/<role ID>
		roleID := role.Name[strings.LastIndex(role.Name, "/")+1:]
		resources = append(resources, terraformutils.NewResource(
			role.Name,
			role.Name,
			"google_project_iam_custom_role",
			g.ProviderName,
			map[string]string{
				"role_id": roleID,
				"project": project,
			}))
	}
	return resources
}

// createIamMemberResources returns a google_project_iam_member for each
// member of each binding, with the ID Terraform imports it by: the project,
// the role and the member, and the condition's title for a conditional
// binding, separated by spaces.
func (g *IamGenerator) createIamMemberResources(policy *cloudresourcemanager.Policy, project string) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, b := range policy.Bindings {
		for _, m := range b.Members {
			id := project + " " + b.Role + " " + m
			name := b.Role + "_" + m
			if b.Condition != nil && b.Condition.Title != "" {
				id += " " + b.Condition.Title
				name += "_" + b.Condition.Title
			}
			resources = append(resources, terraformutils.NewResource(
				id,
				name,
				"google_project_iam_member",
				g.ProviderName,
				map[string]string{
					"role":    b.Role,
					"project": project,
					"member":  m,
				}))
		}
	}
	return resources
}

// InitResources lists the project's service accounts, custom roles and IAM
// members through the IAM and Resource Manager REST APIs.
func (g *IamGenerator) InitResources() error {
	ctx := g.Context()
	project := g.GetArgs()["project"].(string)

	service, err := iam.NewService(ctx, clientOptions()...)
	if err != nil {
		return err
	}
	accounts, roles, err := listIam(ctx, service, project)
	if err != nil {
		return err
	}

	cm, err := cloudresourcemanager.NewService(ctx, clientOptions()...)
	if err != nil {
		return err
	}
	// Version 3 returns conditional bindings as they are, with their
	// conditions; earlier versions rename their roles.
	policy, err := cm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{
		Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: 3},
	}).Context(ctx).Do()
	if err != nil {
		return err
	}

	g.Resources = g.createServiceAccountResources(accounts)
	g.Resources = append(g.Resources, g.createIamCustomRoleResources(roles, project)...)
	g.Resources = append(g.Resources, g.createIamMemberResources(policy, project)...)
	return nil
}

// listIam returns every page of the project's service accounts and custom
// roles.
func listIam(ctx context.Context, service *iam.Service, project string) ([]*iam.ServiceAccount, []*iam.Role, error) {
	var accounts []*iam.ServiceAccount
	err := service.Projects.ServiceAccounts.List("projects/"+project).Pages(ctx, func(page *iam.ListServiceAccountsResponse) error {
		accounts = append(accounts, page.Accounts...)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var roles []*iam.Role
	err = service.Projects.Roles.List("projects/"+project).Pages(ctx, func(page *iam.ListRolesResponse) error {
		roles = append(roles, page.Roles...)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return accounts, roles, nil
}

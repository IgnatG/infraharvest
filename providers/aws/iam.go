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

package aws

import (
	"log"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
)

type IamGenerator struct {
	AWSService
}

func (g *IamGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := iam.NewFromConfig(config)
	g.Resources = []terraformutils.Resource{}
	err := g.getUsers(svc)
	if err != nil {
		log.Println(err)
	}

	err = g.getGroups(svc)
	if err != nil {
		log.Println(err)
	}

	err = g.getPolicies(svc)
	if err != nil {
		log.Println(err)
	}

	err = g.getRoles(svc)
	if err != nil {
		log.Println(err)
	}

	err = g.getInstanceProfiles(svc)
	if err != nil {
		log.Println(err)
	}

	return nil
}

func (g *IamGenerator) getRoles(svc *iam.Client) error {
	p := iam.NewListRolesPaginator(svc, &iam.ListRolesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, role := range page.Roles {
			roleName := StringValue(role.RoleName)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				roleName,
				roleName,
				"aws_iam_role",
				"aws"))
			rolePoliciesPage := iam.NewListRolePoliciesPaginator(svc, &iam.ListRolePoliciesInput{RoleName: role.RoleName}, stopOnDuplicateToken)
			for rolePoliciesPage.HasMorePages() {
				rolePoliciesNextPage, err := rolePoliciesPage.NextPage(g.Context())
				if err != nil {
					return err
				}
				for _, policyName := range rolePoliciesNextPage.PolicyNames {
					g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
						roleName+":"+policyName,
						roleName+"_"+policyName,
						"aws_iam_role_policy",
						"aws"))
				}
			}
			roleAttachedPoliciesPage := iam.NewListAttachedRolePoliciesPaginator(svc, &iam.ListAttachedRolePoliciesInput{
				RoleName: &roleName,
			}, stopOnDuplicateToken)
			for roleAttachedPoliciesPage.HasMorePages() {
				roleAttachedPoliciesNextPage, err := roleAttachedPoliciesPage.NextPage(g.Context())
				if err != nil {
					return err
				}
				for _, attachedPolicy := range roleAttachedPoliciesNextPage.AttachedPolicies {
					g.Resources = append(g.Resources, terraformutils.NewResource(
						roleName+"/"+*attachedPolicy.PolicyArn,
						roleName+"_"+*attachedPolicy.PolicyName,
						"aws_iam_role_policy_attachment",
						"aws",
						map[string]string{
							"role":       roleName,
							"policy_arn": *attachedPolicy.PolicyArn,
						}))
				}
			}
		}
	}
	return nil
}

func (g *IamGenerator) getUsers(svc *iam.Client) error {
	p := iam.NewListUsersPaginator(svc, &iam.ListUsersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, user := range page.Users {
			resourceName := StringValue(user.UserName)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				resourceName,
				StringValue(user.UserId),
				"aws_iam_user",
				"aws",
				map[string]string{
					"force_destroy": "false",
				}))
			err := g.getUserPolices(svc, user.UserName)
			if err != nil {
				log.Println(err)
			}
			err = g.getUserPolicyAttachment(svc, user.UserName)
			if err != nil {
				log.Println(err)
			}
			err = g.getUserGroup(svc, user.UserName)
			if err != nil {
				log.Println(err)
			}
			err = g.getUserAccessKey(svc, user.UserName)
			if err != nil {
				log.Println(err)
			}
		}
	}
	return nil
}

func (g *IamGenerator) getUserGroup(svc *iam.Client, userName *string) error {
	p := iam.NewListGroupsForUserPaginator(svc, &iam.ListGroupsForUserInput{UserName: userName}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.Groups {
			userGroupMembership := *userName + "/" + *group.GroupName
			g.Resources = append(g.Resources, terraformutils.NewResource(
				userGroupMembership,
				userGroupMembership,
				"aws_iam_user_group_membership",
				"aws",
				map[string]string{
					"user":     *userName,
					"groups.#": "1",
					"groups.0": *group.GroupName,
				}))
		}
	}
	return nil
}

func (g *IamGenerator) getUserPolices(svc *iam.Client, userName *string) error {
	p := iam.NewListUserPoliciesPaginator(svc, &iam.ListUserPoliciesInput{UserName: userName}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, policy := range page.PolicyNames {
			resourceName := StringValue(userName) + "_" + policy
			resourceName = strings.ReplaceAll(resourceName, "@", "")
			policyID := StringValue(userName) + ":" + policy
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				policyID,
				resourceName,
				"aws_iam_user_policy",
				"aws"))
		}
	}
	return nil
}

func (g *IamGenerator) getUserPolicyAttachment(svc *iam.Client, userName *string) error {
	p := iam.NewListAttachedUserPoliciesPaginator(svc, &iam.ListAttachedUserPoliciesInput{
		UserName: userName,
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, attachedPolicy := range page.AttachedPolicies {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				*userName+"/"+*attachedPolicy.PolicyArn,
				*userName+"_"+*attachedPolicy.PolicyName,
				"aws_iam_user_policy_attachment",
				"aws",
				map[string]string{
					"user":       *userName,
					"policy_arn": *attachedPolicy.PolicyArn,
				}))
		}
	}
	return nil
}

func (g *IamGenerator) getPolicies(svc *iam.Client) error {
	p := iam.NewListPoliciesPaginator(svc, &iam.ListPoliciesInput{Scope: types.PolicyScopeTypeLocal}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, policy := range page.Policies {
			resourceName := StringValue(policy.PolicyName)
			policyARN := StringValue(policy.Arn)

			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				policyARN,
				resourceName,
				"aws_iam_policy",
				"aws"))
		}
	}
	return nil
}

func (g *IamGenerator) getGroups(svc *iam.Client) error {
	p := iam.NewListGroupsPaginator(svc, &iam.ListGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.Groups {
			resourceName := StringValue(group.GroupName)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_iam_group",
				"aws"))
			if err := g.getGroupPolicies(svc, group); err != nil {
				return err
			}
			if err := g.getAttachedGroupPolicies(svc, group); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *IamGenerator) getGroupPolicies(svc *iam.Client, group types.Group) error {
	groupPoliciesPage := iam.NewListGroupPoliciesPaginator(svc, &iam.ListGroupPoliciesInput{GroupName: group.GroupName}, stopOnDuplicateToken)
	for groupPoliciesPage.HasMorePages() {
		groupPoliciesNextPage, err := groupPoliciesPage.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, policy := range groupPoliciesNextPage.PolicyNames {
			id := *group.GroupName + ":" + policy
			groupPolicyName := *group.GroupName + "_" + policy
			g.Resources = append(g.Resources, terraformutils.NewResource(
				id,
				groupPolicyName,
				"aws_iam_group_policy",
				"aws",
				map[string]string{}))
		}
	}
	return nil
}

func (g *IamGenerator) getAttachedGroupPolicies(svc *iam.Client, group types.Group) error {
	groupAttachedPoliciesPage := iam.NewListAttachedGroupPoliciesPaginator(svc,
		&iam.ListAttachedGroupPoliciesInput{GroupName: group.GroupName}, stopOnDuplicateToken)
	for groupAttachedPoliciesPage.HasMorePages() {
		groupAttachedPoliciesNextPage, err := groupAttachedPoliciesPage.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, attachedPolicy := range groupAttachedPoliciesNextPage.AttachedPolicies {
			if !strings.Contains(*attachedPolicy.PolicyArn, "arn:aws:iam::aws") {
				continue // map only AWS managed policies since others should be managed by
			}
			id := *group.GroupName + "/" + *attachedPolicy.PolicyArn
			g.Resources = append(g.Resources, terraformutils.NewResource(
				id,
				*group.GroupName+"_"+*attachedPolicy.PolicyName,
				"aws_iam_group_policy_attachment",
				"aws",
				map[string]string{
					"group":      *group.GroupName,
					"policy_arn": *attachedPolicy.PolicyArn,
				}))
		}
	}
	return nil
}

func (g *IamGenerator) getInstanceProfiles(svc *iam.Client) error {
	p := iam.NewListInstanceProfilesPaginator(svc, &iam.ListInstanceProfilesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, instanceProfile := range page.InstanceProfiles {
			resourceName := *instanceProfile.InstanceProfileName

			g.Resources = append(g.Resources, terraformutils.NewResource(
				resourceName,
				resourceName,
				"aws_iam_instance_profile",
				"aws",
				map[string]string{
					"name": resourceName,
				}))
		}
	}
	return nil
}

func (g *IamGenerator) getUserAccessKey(svc *iam.Client, userName *string) error {
	p := iam.NewListAccessKeysPaginator(svc, &iam.ListAccessKeysInput{UserName: userName}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, key := range page.AccessKeyMetadata {
			accessKeyID := StringValue(key.AccessKeyId)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				accessKeyID,
				accessKeyID,
				"aws_iam_access_key",
				"aws",
				map[string]string{
					"user": *userName,
				}))
		}
	}
	return nil
}

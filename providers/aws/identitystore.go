// Copyright 2019 The Terraformer Authors.
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
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	"github.com/aws/aws-sdk-go-v2/service/identitystore/types"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
)

type IdentityStoreGenerator struct {
	AWSService
}

func (g *IdentityStoreGenerator) GetIdentityStoreID() (*string, error) {
	config, e := g.generateConfig()
	if e != nil {
		return nil, e
	}
	svc := ssoadmin.NewFromConfig(config)
	instances, err := svc.ListInstances(g.Context(), &ssoadmin.ListInstancesInput{})
	if err != nil {
		return nil, err
	}
	if len(instances.Instances) == 0 {
		return nil, nil
	}
	identityStoreID := StringValue(instances.Instances[0].IdentityStoreId)
	return &identityStoreID, nil

}

func (g *IdentityStoreGenerator) InitGroupResources(identityStoreID string) error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := identitystore.NewFromConfig(config)
	p := identitystore.NewListGroupsPaginator(svc, &identitystore.ListGroupsInput{
		IdentityStoreId: aws.String(identityStoreID),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.Groups {
			groupID := StringValue(group.GroupId)
			displayName := StringValue(group.DisplayName)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				identityStoreID+"/"+groupID,
				displayName,
				"aws_identitystore_group",
				"aws",
				map[string]string{
					"identity_store_id": identityStoreID,
					"description":       StringValue(group.Description),
				}))
			err = g.InitGroupMembershipResources(identityStoreID, groupID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *IdentityStoreGenerator) InitGroupMembershipResources(identityStoreID string, groupID string) error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := identitystore.NewFromConfig(config)
	p := identitystore.NewListGroupMembershipsPaginator(svc, &identitystore.ListGroupMembershipsInput{
		GroupId:         aws.String(groupID),
		IdentityStoreId: aws.String(identityStoreID),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, user := range page.GroupMemberships {
			var memberID string
			switch v := user.MemberId.(type) {
			case *types.MemberIdMemberUserId:
				memberID = v.Value // Value is string
			case *types.UnknownUnionMember:
				memberID = v.Tag
			default:
				memberID = ""
			}
			membershipID := StringValue(user.MembershipId)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				identityStoreID+"/"+membershipID,
				"m-"+groupID+"-"+memberID,
				"aws_identitystore_group_membership",
				"aws",
				map[string]string{
					"identity_store_id": identityStoreID,
					"group_id":          groupID,
					"member_id":         memberID,
				}))
		}
	}
	return nil
}

func (g *IdentityStoreGenerator) InitUserResources(identityStoreID string) error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := identitystore.NewFromConfig(config)
	p := identitystore.NewListUsersPaginator(svc, &identitystore.ListUsersInput{
		IdentityStoreId: aws.String(identityStoreID),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, user := range page.Users {
			userID := StringValue(user.UserId)
			displayName := StringValue(user.DisplayName)
			//			name := StringValue(user.Name)
			userName := StringValue(user.UserName)
			g.Resources = append(g.Resources, terraformutils.NewResource(
				identityStoreID+"/"+userID,
				userName,
				"aws_identitystore_user",
				"aws",
				map[string]string{
					"identity_store_id": identityStoreID,
					"display_name":      displayName,
					"use_name":          userName,
				}))
		}
	}
	return nil
}

func (g *IdentityStoreGenerator) InitResources() error {
	identityStoreID, e := g.GetIdentityStoreID()
	if e != nil {
		return e
	}
	if identityStoreID == nil {
		return nil
	}

	e = g.InitUserResources(*identityStoreID)
	if e != nil {
		return e
	}

	e = g.InitGroupResources(*identityStoreID)
	if e != nil {
		return e
	}

	return nil
}

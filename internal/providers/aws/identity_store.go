// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/identitystore"
	idstypes "github.com/aws/aws-sdk-go-v2/service/identitystore/types"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// IdentityStoreLister lists the users, groups and group memberships of
// the identity store behind IAM Identity Center (service identitystore).
type IdentityStoreLister struct {
	Service
}

// List finds the identity store of the first IAM Identity Center
// instance, then lists its users, then its groups, each followed by its
// memberships. Without an instance there is nothing to list. Every error
// is returned.
func (g *IdentityStoreLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *IdentityStoreLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	instances, err := ssoadmin.NewFromConfig(config).ListInstances(g.Context(), &ssoadmin.ListInstancesInput{})
	if err != nil {
		return err
	}
	if len(instances.Instances) == 0 {
		return nil
	}
	// Only the first instance's store is listed.
	store := aws.ToString(instances.Instances[0].IdentityStoreId)
	client := identitystore.NewFromConfig(config)

	if err := g.listUsers(client, store); err != nil {
		return err
	}
	return g.listGroups(client, store)
}

// listUsers records the users of store as <store>/<user ID>, which is how
// Terraform imports them, named after their user name.
func (g *IdentityStoreLister) listUsers(client *identitystore.Client, store string) error {
	pages := identitystore.NewListUsersPaginator(client, &identitystore.ListUsersInput{IdentityStoreId: aws.String(store)}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, user := range page.Users {
			name := aws.ToString(user.UserName)
			g.Resources = append(g.Resources, harvest.Record(
				store+"/"+aws.ToString(user.UserId), name, "aws_identitystore_user", "aws",
				map[string]string{
					"identity_store_id": store,
					"display_name":      aws.ToString(user.DisplayName),
					"user_name":         name,
				}))
		}
	}
	return nil
}

// listGroups records the groups of store as <store>/<group ID>, named
// after their display name, each followed by its memberships.
func (g *IdentityStoreLister) listGroups(client *identitystore.Client, store string) error {
	pages := identitystore.NewListGroupsPaginator(client, &identitystore.ListGroupsInput{IdentityStoreId: aws.String(store)}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.Groups {
			groupID := aws.ToString(group.GroupId)
			g.Resources = append(g.Resources, harvest.Record(
				store+"/"+groupID, aws.ToString(group.DisplayName), "aws_identitystore_group", "aws",
				map[string]string{
					"identity_store_id": store,
					"description":       aws.ToString(group.Description),
				}))
			// A call per group, for memberships the import may not keep.
			if !g.Wants("aws_identitystore_group_membership") {
				continue
			}
			if err := g.listMemberships(client, store, groupID); err != nil {
				return err
			}
		}
	}
	return nil
}

// listMemberships records the memberships of group groupID as
// <store>/<membership ID>, named m-<group ID>-<member ID>.
func (g *IdentityStoreLister) listMemberships(client *identitystore.Client, store, groupID string) error {
	input := &identitystore.ListGroupMembershipsInput{IdentityStoreId: aws.String(store), GroupId: aws.String(groupID)}
	pages := identitystore.NewListGroupMembershipsPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, membership := range page.GroupMemberships {
			member := memberID(membership.MemberId)
			g.Resources = append(g.Resources, harvest.Record(
				store+"/"+aws.ToString(membership.MembershipId), "m-"+groupID+"-"+member, "aws_identitystore_group_membership", "aws",
				map[string]string{
					"identity_store_id": store,
					"group_id":          groupID,
					"member_id":         member,
				}))
		}
	}
	return nil
}

// memberID returns the ID of a group member: the user ID for a user, the
// union tag for a kind of member this SDK doesn't know, and "" when the
// answer names no member.
func memberID(member idstypes.MemberId) string {
	switch m := member.(type) {
	case *idstypes.MemberIdMemberUserId:
		return m.Value
	case *idstypes.UnknownUnionMember:
		return m.Tag
	}
	return ""
}

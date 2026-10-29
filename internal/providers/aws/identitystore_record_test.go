// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists the users, then the groups, each followed by its
// memberships, of the first IAM Identity Center instance's identity store.
// Users, groups and memberships are imported as <store>/<id>.
func TestRecordIdentityStoreListsUsersGroupsAndMemberships(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListInstances":
			// Only the first instance's store is listed.
			return `{"Instances":[
				{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-1"},
				{"InstanceArn":"arn:aws:sso:::instance/ssoins-2","IdentityStoreId":"d-2"}]}`
		case "ListUsers":
			if store := recordJSONField(call, "IdentityStoreId"); store != "d-1" {
				t.Errorf("ListUsers of store %q", store)
			}
			if recordSecondPage(call) {
				return `{"Users":[{"IdentityStoreId":"d-1","UserId":"u-2","UserName":"bob"}]}`
			}
			return `{"Users":[{"IdentityStoreId":"d-1","UserId":"u-1","UserName":"alice","DisplayName":"Alice Smith"}],"NextToken":"` + nextPageToken + `"}`
		case "ListGroups":
			if store := recordJSONField(call, "IdentityStoreId"); store != "d-1" {
				t.Errorf("ListGroups of store %q", store)
			}
			if recordSecondPage(call) {
				return `{"Groups":[{"IdentityStoreId":"d-1","GroupId":"g-2","DisplayName":"readers"}]}`
			}
			return `{"Groups":[{"IdentityStoreId":"d-1","GroupId":"g-1","DisplayName":"admins","Description":"Admins"}],"NextToken":"` + nextPageToken + `"}`
		case "ListGroupMemberships":
			if store := recordJSONField(call, "IdentityStoreId"); store != "d-1" {
				t.Errorf("ListGroupMemberships of store %q", store)
			}
			switch recordJSONField(call, "GroupId") {
			case "g-1":
				if recordSecondPage(call) {
					// A member that isn't a user has no member ID.
					return `{"GroupMemberships":[{"IdentityStoreId":"d-1","MembershipId":"m-2","GroupId":"g-1"}]}`
				}
				return `{"GroupMemberships":[{"IdentityStoreId":"d-1","MembershipId":"m-1","GroupId":"g-1","MemberId":{"UserId":"u-1"}}],"NextToken":"` + nextPageToken + `"}`
			case "g-2":
				return `{"GroupMemberships":[]}`
			}
		}
		return recordUnexpected(t, call)
	})
	g := &IdentityStoreLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_identitystore_user", "d-1/u-1", "alice", map[string]string{
			"identity_store_id": "d-1", "display_name": "Alice Smith", "user_name": "alice"}},
		recordedResource{"aws_identitystore_user", "d-1/u-2", "bob", map[string]string{
			"identity_store_id": "d-1", "display_name": "", "user_name": "bob"}},
		recordedResource{"aws_identitystore_group", "d-1/g-1", "admins", map[string]string{
			"identity_store_id": "d-1", "description": "Admins"}},
		recordedResource{"aws_identitystore_group_membership", "d-1/m-1", "m-g-1-u-1", map[string]string{
			"identity_store_id": "d-1", "group_id": "g-1", "member_id": "u-1"}},
		recordedResource{"aws_identitystore_group_membership", "d-1/m-2", "m-g-1-", map[string]string{
			"identity_store_id": "d-1", "group_id": "g-1", "member_id": ""}},
		recordedResource{"aws_identitystore_group", "d-1/g-2", "readers", map[string]string{
			"identity_store_id": "d-1", "description": ""}},
	)
}

// Without an IAM Identity Center instance there is nothing to list, and no
// error.
func TestRecordIdentityStoreWithoutInstance(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListInstances" {
			return `{"Instances":[]}`
		}
		return recordUnexpected(t, call)
	})
	g := &IdentityStoreLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources)
}

// Every error is returned.
func TestRecordIdentityStoreReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListInstances", "ListUsers", "ListGroups", "ListGroupMemberships"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				if call.Op == failing {
					return recordJSONError("AccessDeniedException")
				}
				switch call.Op {
				case "ListInstances":
					return `{"Instances":[{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-1"}]}`
				case "ListUsers":
					return `{"Users":[]}`
				case "ListGroups":
					return `{"Groups":[{"IdentityStoreId":"d-1","GroupId":"g-1","DisplayName":"admins"}]}`
				case "ListGroupMemberships":
					return `{"GroupMemberships":[]}`
				}
				return recordUnexpected(t, call)
			})
			g := &IdentityStoreLister{}

			_, err := listChecked(ctx, t, g, harvest.Settings{})

			assertRecordedError(t, err, "AccessDeniedException")
		})
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// IamLister lists the IAM users, groups, customer-managed policies,
// roles and instance profiles of the account, with what hangs off them
// (service iam). IAM is global, so it runs once, in the global
// pseudo-region.
type IamLister struct {
	Service
}

// List lists the account's IAM resources section by section:
// users, groups, policies, roles, instance profiles. The first error ends
// the listing and is returned, so that a failed list never passes for an
// empty one; the one exception is an entity deleted between the list that
// named it and the reading of its own lists (see iamGone).
func (g *IamLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := iam.NewFromConfig(config)
	g.Resources = nil

	sections := []struct {
		name string
		list func(*iam.Client) error
	}{
		{"users", g.getUsers},
		{"groups", g.getGroups},
		{"policies", g.getPolicies},
		{"roles", g.getRoles},
		{"instance profiles", g.getInstanceProfiles},
	}
	for _, section := range sections {
		if err := section.list(client); err != nil {
			return fmt.Errorf("%s: %w", section.name, err)
		}
	}
	return nil
}

// iamGone reports whether err says that the entity whose own lists were
// being read, a user, group or role, no longer exists: it was deleted
// after the list that named it, so there is nothing of it left to import.
func iamGone(err error) bool {
	var gone *iamtypes.NoSuchEntityException
	return errors.As(err, &gone)
}

// iamSubList runs list, which reads what (its inline policies, say) of the
// user, group or role (kind) called name. An entity that went away
// meanwhile is logged and the list left out; any other error is returned,
// saying whose list it was.
func iamSubList(what, kind, name string, list func() error) error {
	err := list()
	switch {
	case err == nil:
		return nil
	case iamGone(err):
		log.Printf("aws: iam: %s of %s %s: it no longer exists; leaving it out: %v", what, kind, name, err)
		return nil
	}
	return fmt.Errorf("%s of %s %s: %w", what, kind, name, err)
}

// iamPages is what every IAM paginator offers, for eachIamPage.
type iamPages[O any] interface {
	HasMorePages() bool
	NextPage(ctx context.Context, optFns ...func(*iam.Options)) (O, error)
}

// eachIamPage calls visit with every page of pages, in order, and stops
// at the first error, its own or visit's. A paginator does not move on
// after an error, so asking it again would request the same page forever.
func eachIamPage[O any](ctx context.Context, pages iamPages[O], visit func(page O) error) error {
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		if err := visit(page); err != nil {
			return err
		}
	}
	return nil
}

// add records one IAM resource. Nil attributes are recorded as none, an
// empty map, as later steps add to a resource's attributes.
func (g *IamLister) add(id, name, resourceType string, attributes map[string]string) {
	if attributes == nil {
		attributes = map[string]string{}
	}
	g.Resources = append(g.Resources, harvest.Record(id, name, resourceType, "aws", attributes))
}

// getUsers records every user by its user name, which Terraform imports
// it by, each followed by its inline policies, its policy attachments,
// its group memberships and its access keys.
func (g *IamLister) getUsers(client *iam.Client) error {
	userLists := []struct {
		what string
		list func(*iam.Client, string) error
	}{
		{"inline policies", g.listUserInlinePolicies},
		{"policy attachments", g.listUserAttachments},
		{"group memberships", g.listUserGroupMemberships},
		{"access keys", g.listUserAccessKeys},
	}
	pages := iam.NewListUsersPaginator(client, &iam.ListUsersInput{}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListUsersOutput) error {
		for _, user := range page.Users {
			name := aws.ToString(user.UserName)
			g.add(name, name, "aws_iam_user", map[string]string{"force_destroy": "false"})
			for _, l := range userLists {
				if err := iamSubList(l.what, "user", name, func() error { return l.list(client, name) }); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// listUserInlinePolicies records the inline policies of user as
// <user>:<policy>, which is how Terraform imports them.
func (g *IamLister) listUserInlinePolicies(client *iam.Client, user string) error {
	pages := iam.NewListUserPoliciesPaginator(client, &iam.ListUserPoliciesInput{UserName: aws.String(user)}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListUserPoliciesOutput) error {
		for _, policy := range page.PolicyNames {
			// A user name may hold an @, as an email address does; the
			// name leaves it out.
			g.add(user+":"+policy, strings.ReplaceAll(user+"_"+policy, "@", ""), "aws_iam_user_policy", nil)
		}
		return nil
	})
}

// listUserAttachments records the managed policies attached to user as
// <user>/<policy ARN>.
func (g *IamLister) listUserAttachments(client *iam.Client, user string) error {
	pages := iam.NewListAttachedUserPoliciesPaginator(client, &iam.ListAttachedUserPoliciesInput{UserName: aws.String(user)}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListAttachedUserPoliciesOutput) error {
		for _, policy := range page.AttachedPolicies {
			arn := aws.ToString(policy.PolicyArn)
			g.add(user+"/"+arn, user+"_"+aws.ToString(policy.PolicyName), "aws_iam_user_policy_attachment",
				map[string]string{"user": user, "policy_arn": arn})
		}
		return nil
	})
}

// listUserGroupMemberships records one group membership per group user
// belongs to, as <user>/<group>.
func (g *IamLister) listUserGroupMemberships(client *iam.Client, user string) error {
	pages := iam.NewListGroupsForUserPaginator(client, &iam.ListGroupsForUserInput{UserName: aws.String(user)}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListGroupsForUserOutput) error {
		for _, group := range page.Groups {
			name := aws.ToString(group.GroupName)
			id := user + "/" + name
			g.add(id, id, "aws_iam_user_group_membership", map[string]string{
				"user":     user,
				"groups.#": "1",
				"groups.0": name,
			})
		}
		return nil
	})
}

// listUserAccessKeys records the access keys of user by key ID.
func (g *IamLister) listUserAccessKeys(client *iam.Client, user string) error {
	pages := iam.NewListAccessKeysPaginator(client, &iam.ListAccessKeysInput{UserName: aws.String(user)}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListAccessKeysOutput) error {
		for _, key := range page.AccessKeyMetadata {
			id := aws.ToString(key.AccessKeyId)
			g.add(id, id, "aws_iam_access_key", map[string]string{"user": user})
		}
		return nil
	})
}

// getGroups records every group, each followed by its inline policies and
// its policy attachments, AWS managed or not.
func (g *IamLister) getGroups(client *iam.Client) error {
	pages := iam.NewListGroupsPaginator(client, &iam.ListGroupsInput{}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListGroupsOutput) error {
		for _, group := range page.Groups {
			name := aws.ToString(group.GroupName)
			if err := iamSubList("policies", "group", name, func() error { return g.addGroup(client, group) }); err != nil {
				return err
			}
		}
		return nil
	})
}

// addGroup records group, its inline policies (as <group>:<policy>) and
// its policy attachments (as <group>/<policy ARN>).
func (g *IamLister) addGroup(client *iam.Client, group iamtypes.Group) error {
	name := aws.ToString(group.GroupName)
	g.add(name, name, "aws_iam_group", nil)

	inline := iam.NewListGroupPoliciesPaginator(client, &iam.ListGroupPoliciesInput{GroupName: group.GroupName}, stopOnDuplicateToken)
	err := eachIamPage(g.Context(), inline, func(page *iam.ListGroupPoliciesOutput) error {
		for _, policy := range page.PolicyNames {
			g.add(name+":"+policy, name+"_"+policy, "aws_iam_group_policy", nil)
		}
		return nil
	})
	if err != nil {
		return err
	}

	attached := iam.NewListAttachedGroupPoliciesPaginator(client, &iam.ListAttachedGroupPoliciesInput{GroupName: group.GroupName}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), attached, func(page *iam.ListAttachedGroupPoliciesOutput) error {
		for _, policy := range page.AttachedPolicies {
			arn := aws.ToString(policy.PolicyArn)
			g.add(name+"/"+arn, name+"_"+aws.ToString(policy.PolicyName), "aws_iam_group_policy_attachment",
				map[string]string{"group": name, "policy_arn": arn})
		}
		return nil
	})
}

// getPolicies records the customer-managed policies by ARN. AWS managed
// policies exist in every account and are not imported.
func (g *IamLister) getPolicies(client *iam.Client) error {
	input := &iam.ListPoliciesInput{Scope: iamtypes.PolicyScopeTypeLocal}
	pages := iam.NewListPoliciesPaginator(client, input, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListPoliciesOutput) error {
		for _, policy := range page.Policies {
			g.add(aws.ToString(policy.Arn), aws.ToString(policy.PolicyName), "aws_iam_policy", nil)
		}
		return nil
	})
}

// getRoles records every role, each followed by its inline policies and
// its policy attachments, AWS managed or not. Service-linked roles are
// recorded too; the catalogue leaves them out of the default selection.
func (g *IamLister) getRoles(client *iam.Client) error {
	pages := iam.NewListRolesPaginator(client, &iam.ListRolesInput{}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListRolesOutput) error {
		for _, role := range page.Roles {
			name := aws.ToString(role.RoleName)
			if err := iamSubList("policies", "role", name, func() error { return g.addRole(client, role) }); err != nil {
				return err
			}
		}
		return nil
	})
}

// addRole records role, its inline policies (as <role>:<policy>) and its
// policy attachments (as <role>/<policy ARN>).
func (g *IamLister) addRole(client *iam.Client, role iamtypes.Role) error {
	name := aws.ToString(role.RoleName)
	g.add(name, name, "aws_iam_role", nil)

	inline := iam.NewListRolePoliciesPaginator(client, &iam.ListRolePoliciesInput{RoleName: role.RoleName}, stopOnDuplicateToken)
	err := eachIamPage(g.Context(), inline, func(page *iam.ListRolePoliciesOutput) error {
		for _, policy := range page.PolicyNames {
			g.add(name+":"+policy, name+"_"+policy, "aws_iam_role_policy", nil)
		}
		return nil
	})
	if err != nil {
		return err
	}

	attached := iam.NewListAttachedRolePoliciesPaginator(client, &iam.ListAttachedRolePoliciesInput{RoleName: role.RoleName}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), attached, func(page *iam.ListAttachedRolePoliciesOutput) error {
		for _, policy := range page.AttachedPolicies {
			arn := aws.ToString(policy.PolicyArn)
			g.add(name+"/"+arn, name+"_"+aws.ToString(policy.PolicyName), "aws_iam_role_policy_attachment",
				map[string]string{"role": name, "policy_arn": arn})
		}
		return nil
	})
}

// getInstanceProfiles records every instance profile by name.
func (g *IamLister) getInstanceProfiles(client *iam.Client) error {
	pages := iam.NewListInstanceProfilesPaginator(client, &iam.ListInstanceProfilesInput{}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListInstanceProfilesOutput) error {
		for _, profile := range page.InstanceProfiles {
			name := aws.ToString(profile.InstanceProfileName)
			g.add(name, name, "aws_iam_instance_profile", map[string]string{"name": name})
		}
		return nil
	})
}

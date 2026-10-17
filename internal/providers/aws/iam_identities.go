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

// List lists the account's IAM resources section by section: users,
// groups, policies, roles, instance profiles. Users, groups, policies and
// roles, with their inline policies, policy attachments and group
// memberships, all come from the account's authorization details, a few
// pages for the whole account rather than a few lists per user, group and
// role; only access keys are still listed user by user. The first error
// ends the listing and is returned, so that a failed list never passes for
// an empty one; the one exception is a user deleted between the details
// that named it and the listing of its access keys (see iamGone).
func (g *IamLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := iam.NewFromConfig(config)
	g.Resources = nil

	details, err := g.getAuthorizationDetails(client)
	if err != nil {
		return fmt.Errorf("authorization details: %w", err)
	}
	if err := g.addUsers(client, details.users); err != nil {
		return fmt.Errorf("users: %w", err)
	}
	g.addGroups(details.groups)
	g.addPolicies(details.policies)
	g.addRoles(details.roles)
	if err := g.getInstanceProfiles(client); err != nil {
		return fmt.Errorf("instance profiles: %w", err)
	}
	return nil
}

// iamGone reports whether err says that the user whose access keys were
// being listed no longer exists: it was deleted after the details that
// named it, so there is nothing of it left to import.
func iamGone(err error) bool {
	var gone *iamtypes.NoSuchEntityException
	return errors.As(err, &gone)
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

// iamAuthorizationDetails is every page of the account's authorization
// details put together: its users, groups, customer-managed policies and
// roles, each with its inline policies and policy attachments.
type iamAuthorizationDetails struct {
	users    []iamtypes.UserDetail
	groups   []iamtypes.GroupDetail
	policies []iamtypes.ManagedPolicyDetail
	roles    []iamtypes.RoleDetail
}

// iamPageSize is the most items IAM answers a list with; without it, IAM
// answers 100 at a time.
const iamPageSize = 1000

// getAuthorizationDetails reads the account's authorization details, all
// pages of them, leaving out the AWS managed policies, which exist in every
// account and are not imported. A page mixes users, groups, policies and
// roles, so all pages are read before anything is recorded, to record them
// kind by kind. Each page holds the most IAM allows, iamPageSize.
func (g *IamLister) getAuthorizationDetails(client *iam.Client) (iamAuthorizationDetails, error) {
	var details iamAuthorizationDetails
	input := &iam.GetAccountAuthorizationDetailsInput{Filter: []iamtypes.EntityType{
		iamtypes.EntityTypeUser,
		iamtypes.EntityTypeGroup,
		iamtypes.EntityTypeLocalManagedPolicy,
		iamtypes.EntityTypeRole,
	}, MaxItems: aws.Int32(iamPageSize)}
	pages := iam.NewGetAccountAuthorizationDetailsPaginator(client, input, stopOnDuplicateToken)
	err := eachIamPage(g.Context(), pages, func(page *iam.GetAccountAuthorizationDetailsOutput) error {
		details.users = append(details.users, page.UserDetailList...)
		details.groups = append(details.groups, page.GroupDetailList...)
		details.policies = append(details.policies, page.Policies...)
		details.roles = append(details.roles, page.RoleDetailList...)
		return nil
	})
	return details, err
}

// addUsers records every user by its user name, which Terraform imports
// it by, each followed by its inline policies (as <user>:<policy>), its
// policy attachments (as <user>/<policy ARN>), its group memberships (as
// <user>/<group>) and its access keys, the one list read user by user. A
// user deleted before its access keys are listed is logged and its keys
// left out.
func (g *IamLister) addUsers(client *iam.Client, users []iamtypes.UserDetail) error {
	for _, user := range users {
		name := aws.ToString(user.UserName)
		g.add(name, name, "aws_iam_user", withIamTags(map[string]string{"force_destroy": "false"}, user.Tags))
		for _, policy := range user.UserPolicyList {
			inline := aws.ToString(policy.PolicyName)
			// A user name may hold an @, as an email address does; the
			// name leaves it out.
			g.add(name+":"+inline, strings.ReplaceAll(name+"_"+inline, "@", ""), "aws_iam_user_policy", nil)
		}
		for _, policy := range user.AttachedManagedPolicies {
			arn := aws.ToString(policy.PolicyArn)
			g.add(name+"/"+arn, name+"_"+aws.ToString(policy.PolicyName), "aws_iam_user_policy_attachment",
				map[string]string{"user": name, "policy_arn": arn})
		}
		for _, group := range user.GroupList {
			id := name + "/" + group
			g.add(id, id, "aws_iam_user_group_membership", map[string]string{
				"user":     name,
				"groups.#": "1",
				"groups.0": group,
			})
		}
		// One call per user, for keys the default selection leaves out.
		if !g.wants("aws_iam_access_key") {
			continue
		}
		if err := g.listUserAccessKeys(client, name); err != nil {
			if !iamGone(err) {
				return fmt.Errorf("access keys of user %s: %w", name, err)
			}
			log.Printf("aws: iam: access keys of user %s: it no longer exists; leaving them out: %v", name, err)
		}
	}
	return nil
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

// addGroups records every group, each followed by its inline policies (as
// <group>:<policy>) and its policy attachments (as <group>/<policy ARN>),
// AWS managed or not.
func (g *IamLister) addGroups(groups []iamtypes.GroupDetail) {
	for _, group := range groups {
		name := aws.ToString(group.GroupName)
		g.add(name, name, "aws_iam_group", nil)
		for _, policy := range group.GroupPolicyList {
			inline := aws.ToString(policy.PolicyName)
			g.add(name+":"+inline, name+"_"+inline, "aws_iam_group_policy", nil)
		}
		for _, policy := range group.AttachedManagedPolicies {
			arn := aws.ToString(policy.PolicyArn)
			g.add(name+"/"+arn, name+"_"+aws.ToString(policy.PolicyName), "aws_iam_group_policy_attachment",
				map[string]string{"group": name, "policy_arn": arn})
		}
	}
}

// addPolicies records the customer-managed policies by ARN.
func (g *IamLister) addPolicies(policies []iamtypes.ManagedPolicyDetail) {
	for _, policy := range policies {
		g.add(aws.ToString(policy.Arn), aws.ToString(policy.PolicyName), "aws_iam_policy", nil)
	}
}

// addRoles records every role, each followed by its inline policies (as
// <role>:<policy>) and its policy attachments (as <role>/<policy ARN>), AWS
// managed or not. Service-linked roles are recorded too; the catalogue
// leaves them out of the default selection.
func (g *IamLister) addRoles(roles []iamtypes.RoleDetail) {
	for _, role := range roles {
		name := aws.ToString(role.RoleName)
		g.add(name, name, "aws_iam_role", withIamTags(nil, role.Tags))
		for _, policy := range role.RolePolicyList {
			inline := aws.ToString(policy.PolicyName)
			g.add(name+":"+inline, name+"_"+inline, "aws_iam_role_policy", nil)
		}
		for _, policy := range role.AttachedManagedPolicies {
			arn := aws.ToString(policy.PolicyArn)
			g.add(name+"/"+arn, name+"_"+aws.ToString(policy.PolicyName), "aws_iam_role_policy_attachment",
				map[string]string{"role": name, "policy_arn": arn})
		}
	}
}

// getInstanceProfiles records every instance profile by name, iamPageSize
// to a page.
func (g *IamLister) getInstanceProfiles(client *iam.Client) error {
	pages := iam.NewListInstanceProfilesPaginator(client, &iam.ListInstanceProfilesInput{MaxItems: aws.Int32(iamPageSize)}, stopOnDuplicateToken)
	return eachIamPage(g.Context(), pages, func(page *iam.ListInstanceProfilesOutput) error {
		for _, profile := range page.InstanceProfiles {
			name := aws.ToString(profile.InstanceProfileName)
			g.add(name, name, "aws_iam_instance_profile", map[string]string{"name": name})
		}
		return nil
	})
}

// withIamTags adds an IAM user's or role's tags to attributes as
// tags.<key>, which the authorization details carry at no extra call, so
// that the default selection sees them before generating anything, such
// as a ManagedBy tag (see engine.OwnedBy). It returns attributes, or a
// new map when that is nil.
func withIamTags(attributes map[string]string, tags []iamtypes.Tag) map[string]string {
	if attributes == nil {
		attributes = map[string]string{}
	}
	for _, tag := range tags {
		attributes["tags."+aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return attributes
}

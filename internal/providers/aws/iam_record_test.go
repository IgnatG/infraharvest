// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"testing"
)

const (
	recordIamNS       = "https://iam.amazonaws.com/doc/2010-05-08/"
	recordIamAWS      = "arn:aws:iam::aws:policy/"
	recordIamCustomer = "arn:aws:iam::123456789012:policy/"
)

// recordIamList answers an IAM list with items under key: items alone when
// more is empty, otherwise items on the first page and more on the second.
func recordIamList(call apiCall, key, items, more string) string {
	if more == "" {
		return recordQuery(call.Op, recordIamNS, `<`+key+`>`+items+`</`+key+`><IsTruncated>false</IsTruncated>`)
	}
	if recordSecondPage(call) {
		return recordQuery(call.Op, recordIamNS, `<`+key+`>`+more+`</`+key+`><IsTruncated>false</IsTruncated>`)
	}
	return recordQuery(call.Op, recordIamNS, `<`+key+`>`+items+`</`+key+`><IsTruncated>true</IsTruncated><Marker>`+nextPageToken+`</Marker>`)
}

func recordIamUser(name, id string) string {
	return `<member><UserName>` + name + `</UserName><UserId>` + id + `</UserId><Path>/</Path>
		<Arn>arn:aws:iam::123456789012:user/` + name + `</Arn></member>`
}

func recordIamGroup(name string) string {
	return `<member><GroupName>` + name + `</GroupName><GroupId>AGPA` + name + `</GroupId><Path>/</Path>
		<Arn>arn:aws:iam::123456789012:group/` + name + `</Arn></member>`
}

func recordIamRole(name string) string {
	return `<member><RoleName>` + name + `</RoleName><RoleId>AROA` + name + `</RoleId><Path>/</Path>
		<Arn>arn:aws:iam::123456789012:role/` + name + `</Arn></member>`
}

func recordIamAttached(name, arn string) string {
	return `<member><PolicyName>` + name + `</PolicyName><PolicyArn>` + arn + `</PolicyArn></member>`
}

func recordIamName(name string) string {
	return `<member>` + name + `</member>`
}

// The lister lists, each list across pages: users, each followed by its
// inline policies, policy attachments, group memberships and access keys;
// groups, each followed by its inline policies and policy attachments;
// customer-managed policies; roles, each followed by its inline policies
// and policy attachments; instance profiles. A user deleted while it is
// listed has the lists it no longer has left out.
func TestRecordIamListsUsersGroupsPoliciesRolesAndProfiles(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		user, group, role := form.Get("UserName"), form.Get("GroupName"), form.Get("RoleName")
		switch {
		case call.Op == "ListUsers":
			return recordIamList(call, "Users", recordIamUser("alice@example.com", "AIDAALICE"), recordIamUser("bob", "AIDABOB"))
		case call.Op == "ListUserPolicies" && user == "alice@example.com":
			return recordIamList(call, "PolicyNames", recordIamName("s3-inline"), recordIamName("kms-inline"))
		case call.Op == "ListUserPolicies" && user == "bob":
			return recordQueryError(recordIamNS, "NoSuchEntity")
		case call.Op == "ListAttachedUserPolicies" && user == "alice@example.com":
			return recordIamList(call, "AttachedPolicies",
				recordIamAttached("ReadOnlyAccess", recordIamAWS+"ReadOnlyAccess"), recordIamAttached("custom", recordIamCustomer+"custom"))
		case call.Op == "ListGroupsForUser" && user == "alice@example.com":
			return recordIamList(call, "Groups", recordIamGroup("admins"), recordIamGroup("devs"))
		case call.Op == "ListAccessKeys" && user == "alice@example.com":
			return recordIamList(call, "AccessKeyMetadata",
				`<member><UserName>alice@example.com</UserName><AccessKeyId>AKIAALICE1</AccessKeyId><Status>Active</Status></member>`,
				`<member><UserName>alice@example.com</UserName><AccessKeyId>AKIAALICE2</AccessKeyId><Status>Inactive</Status></member>`)
		case call.Op == "ListAttachedUserPolicies" && user == "bob":
			return recordIamList(call, "AttachedPolicies", "", "")
		case call.Op == "ListGroupsForUser" && user == "bob":
			return recordIamList(call, "Groups", "", "")
		case call.Op == "ListAccessKeys" && user == "bob":
			return recordIamList(call, "AccessKeyMetadata", "", "")

		case call.Op == "ListGroups":
			return recordIamList(call, "Groups", recordIamGroup("admins"), recordIamGroup("devs"))
		case call.Op == "ListGroupPolicies" && group == "admins":
			return recordIamList(call, "PolicyNames", recordIamName("admin-inline"), recordIamName("audit-inline"))
		case call.Op == "ListAttachedGroupPolicies" && group == "admins":
			return recordIamList(call, "AttachedPolicies",
				recordIamAttached("AdministratorAccess", recordIamAWS+"AdministratorAccess")+recordIamAttached("custom", recordIamCustomer+"custom"),
				recordIamAttached("IAMReadOnlyAccess", recordIamAWS+"IAMReadOnlyAccess"))
		case call.Op == "ListGroupPolicies" && group == "devs":
			return recordIamList(call, "PolicyNames", "", "")
		case call.Op == "ListAttachedGroupPolicies" && group == "devs":
			return recordIamList(call, "AttachedPolicies", "", "")

		case call.Op == "ListPolicies":
			if form.Get("Scope") != "Local" {
				t.Errorf("policies of scope %q, want Local", form.Get("Scope"))
			}
			return recordIamList(call, "Policies",
				`<member><PolicyName>custom</PolicyName><PolicyId>ANPA1</PolicyId><Arn>`+recordIamCustomer+`custom</Arn><AttachmentCount>3</AttachmentCount></member>`,
				`<member><PolicyName>boundary</PolicyName><PolicyId>ANPA2</PolicyId><Arn>`+recordIamCustomer+`boundary</Arn><AttachmentCount>0</AttachmentCount></member>`)

		case call.Op == "ListRoles":
			return recordIamList(call, "Roles", recordIamRole("app"), recordIamRole("AWSServiceRoleForSupport"))
		case call.Op == "ListRolePolicies" && role == "app":
			return recordIamList(call, "PolicyNames", recordIamName("inline-sqs"), recordIamName("inline-sns"))
		case call.Op == "ListAttachedRolePolicies" && role == "app":
			return recordIamList(call, "AttachedPolicies",
				recordIamAttached("custom", recordIamCustomer+"custom"), recordIamAttached("AmazonS3ReadOnlyAccess", recordIamAWS+"AmazonS3ReadOnlyAccess"))
		case call.Op == "ListRolePolicies" && role == "AWSServiceRoleForSupport":
			return recordIamList(call, "PolicyNames", "", "")
		case call.Op == "ListAttachedRolePolicies" && role == "AWSServiceRoleForSupport":
			return recordIamList(call, "AttachedPolicies",
				recordIamAttached("AWSSupportServiceRolePolicy", recordIamAWS+"aws-service-role/AWSSupportServiceRolePolicy"), "")

		case call.Op == "ListInstanceProfiles":
			return recordIamList(call, "InstanceProfiles",
				`<member><InstanceProfileName>app-profile</InstanceProfileName><InstanceProfileId>AIPA1</InstanceProfileId><Path>/</Path>
					<Arn>arn:aws:iam::123456789012:instance-profile/app-profile</Arn><Roles>`+recordIamRole("app")+`</Roles></member>`,
				`<member><InstanceProfileName>batch-profile</InstanceProfileName><InstanceProfileId>AIPA2</InstanceProfileId><Path>/</Path>
					<Arn>arn:aws:iam::123456789012:instance-profile/batch-profile</Arn><Roles/></member>`)
		}
		return recordUnexpected(t, call)
	})
	g := &IamLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_iam_user", "alice@example.com", "alice@example.com", map[string]string{"force_destroy": "false"}},
		recordedResource{"aws_iam_user_policy", "alice@example.com:s3-inline", "aliceexample.com_s3-inline", nil},
		recordedResource{"aws_iam_user_policy", "alice@example.com:kms-inline", "aliceexample.com_kms-inline", nil},
		recordedResource{"aws_iam_user_policy_attachment", "alice@example.com/" + recordIamAWS + "ReadOnlyAccess", "alice@example.com_ReadOnlyAccess",
			map[string]string{"user": "alice@example.com", "policy_arn": recordIamAWS + "ReadOnlyAccess"}},
		recordedResource{"aws_iam_user_policy_attachment", "alice@example.com/" + recordIamCustomer + "custom", "alice@example.com_custom",
			map[string]string{"user": "alice@example.com", "policy_arn": recordIamCustomer + "custom"}},
		recordedResource{"aws_iam_user_group_membership", "alice@example.com/admins", "alice@example.com/admins",
			map[string]string{"user": "alice@example.com", "groups.#": "1", "groups.0": "admins"}},
		recordedResource{"aws_iam_user_group_membership", "alice@example.com/devs", "alice@example.com/devs",
			map[string]string{"user": "alice@example.com", "groups.#": "1", "groups.0": "devs"}},
		recordedResource{"aws_iam_access_key", "AKIAALICE1", "AKIAALICE1", map[string]string{"user": "alice@example.com"}},
		recordedResource{"aws_iam_access_key", "AKIAALICE2", "AKIAALICE2", map[string]string{"user": "alice@example.com"}},
		recordedResource{"aws_iam_user", "bob", "bob", map[string]string{"force_destroy": "false"}},

		recordedResource{"aws_iam_group", "admins", "admins", nil},
		recordedResource{"aws_iam_group_policy", "admins:admin-inline", "admins_admin-inline", nil},
		recordedResource{"aws_iam_group_policy", "admins:audit-inline", "admins_audit-inline", nil},
		recordedResource{"aws_iam_group_policy_attachment", "admins/" + recordIamAWS + "AdministratorAccess", "admins_AdministratorAccess",
			map[string]string{"group": "admins", "policy_arn": recordIamAWS + "AdministratorAccess"}},
		recordedResource{"aws_iam_group_policy_attachment", "admins/" + recordIamCustomer + "custom", "admins_custom",
			map[string]string{"group": "admins", "policy_arn": recordIamCustomer + "custom"}},
		recordedResource{"aws_iam_group_policy_attachment", "admins/" + recordIamAWS + "IAMReadOnlyAccess", "admins_IAMReadOnlyAccess",
			map[string]string{"group": "admins", "policy_arn": recordIamAWS + "IAMReadOnlyAccess"}},
		recordedResource{"aws_iam_group", "devs", "devs", nil},

		recordedResource{"aws_iam_policy", recordIamCustomer + "custom", "custom", nil},
		recordedResource{"aws_iam_policy", recordIamCustomer + "boundary", "boundary", nil},

		recordedResource{"aws_iam_role", "app", "app", nil},
		recordedResource{"aws_iam_role_policy", "app:inline-sqs", "app_inline-sqs", nil},
		recordedResource{"aws_iam_role_policy", "app:inline-sns", "app_inline-sns", nil},
		recordedResource{"aws_iam_role_policy_attachment", "app/" + recordIamCustomer + "custom", "app_custom",
			map[string]string{"role": "app", "policy_arn": recordIamCustomer + "custom"}},
		recordedResource{"aws_iam_role_policy_attachment", "app/" + recordIamAWS + "AmazonS3ReadOnlyAccess", "app_AmazonS3ReadOnlyAccess",
			map[string]string{"role": "app", "policy_arn": recordIamAWS + "AmazonS3ReadOnlyAccess"}},
		recordedResource{"aws_iam_role", "AWSServiceRoleForSupport", "AWSServiceRoleForSupport", nil},
		recordedResource{"aws_iam_role_policy_attachment", "AWSServiceRoleForSupport/" + recordIamAWS + "aws-service-role/AWSSupportServiceRolePolicy",
			"AWSServiceRoleForSupport_AWSSupportServiceRolePolicy",
			map[string]string{"role": "AWSServiceRoleForSupport", "policy_arn": recordIamAWS + "aws-service-role/AWSSupportServiceRolePolicy"}},

		recordedResource{"aws_iam_instance_profile", "app-profile", "app-profile", map[string]string{"name": "app-profile"}},
		recordedResource{"aws_iam_instance_profile", "batch-profile", "batch-profile", map[string]string{"name": "batch-profile"}},
	)
}

// Every list's error is returned, a user's, group's or role's own lists
// included: a failed list must not pass for an empty one.
func TestRecordIamReturnsErrors(t *testing.T) {
	for _, failing := range []string{
		"ListUsers", "ListUserPolicies", "ListAttachedUserPolicies", "ListGroupsForUser", "ListAccessKeys",
		"ListGroups", "ListGroupPolicies", "ListAttachedGroupPolicies", "ListPolicies",
		"ListRoles", "ListRolePolicies", "ListAttachedRolePolicies", "ListInstanceProfiles",
	} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordQueryError(recordIamNS, "AccessDenied")
				case "ListUsers":
					return recordIamList(call, "Users", recordIamUser("alice", "AIDAALICE"), "")
				case "ListUserPolicies", "ListGroupPolicies", "ListRolePolicies":
					return recordIamList(call, "PolicyNames", "", "")
				case "ListAttachedUserPolicies", "ListAttachedGroupPolicies", "ListAttachedRolePolicies":
					return recordIamList(call, "AttachedPolicies", "", "")
				case "ListGroupsForUser":
					return recordIamList(call, "Groups", "", "")
				case "ListAccessKeys":
					return recordIamList(call, "AccessKeyMetadata", "", "")
				case "ListGroups":
					return recordIamList(call, "Groups", recordIamGroup("admins"), "")
				case "ListPolicies":
					return recordIamList(call, "Policies", "", "")
				case "ListRoles":
					return recordIamList(call, "Roles", recordIamRole("app"), "")
				case "ListInstanceProfiles":
					return recordIamList(call, "InstanceProfiles", "", "")
				}
				return recordUnexpected(t, call)
			})
			g := &IamLister{}

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}

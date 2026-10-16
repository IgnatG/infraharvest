// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"slices"
	"testing"
)

const (
	recordIamNS       = "https://iam.amazonaws.com/doc/2010-05-08/"
	recordIamAWS      = "arn:aws:iam::aws:policy/"
	recordIamCustomer = "arn:aws:iam::123456789012:policy/"
	// recordIamDocument is a policy document as the authorization details
	// carry it, URL-encoded.
	recordIamDocument = `%7B%22Version%22%3A%222012-10-17%22%2C%22Statement%22%3A%5B%5D%7D`
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

// recordIamDetails answers GetAccountAuthorizationDetails with the lists
// of details page alone when more is empty, otherwise with page on the
// first page and more on the second.
func recordIamDetails(call apiCall, page, more string) string {
	if more == "" {
		return recordQuery(call.Op, recordIamNS, page+`<IsTruncated>false</IsTruncated>`)
	}
	if recordSecondPage(call) {
		return recordQuery(call.Op, recordIamNS, more+`<IsTruncated>false</IsTruncated>`)
	}
	return recordQuery(call.Op, recordIamNS, page+`<IsTruncated>true</IsTruncated><Marker>`+nextPageToken+`</Marker>`)
}

// recordIamUser is the details of the user name, with its own lists.
func recordIamUser(name, id, lists string) string {
	return `<member><UserName>` + name + `</UserName><UserId>` + id + `</UserId><Path>/</Path>
		<Arn>arn:aws:iam::123456789012:user/` + name + `</Arn>` + lists + `</member>`
}

// recordIamGroup is the details of the group name, with its own lists.
func recordIamGroup(name, lists string) string {
	return `<member><GroupName>` + name + `</GroupName><GroupId>AGPA` + name + `</GroupId><Path>/</Path>
		<Arn>arn:aws:iam::123456789012:group/` + name + `</Arn>` + lists + `</member>`
}

// recordIamRole is the details of the role name, with its own lists.
func recordIamRole(name, lists string) string {
	return `<member><RoleName>` + name + `</RoleName><RoleId>AROA` + name + `</RoleId><Path>/</Path>
		<Arn>arn:aws:iam::123456789012:role/` + name + `</Arn>` + lists + `</member>`
}

// recordIamPolicy is the details of the customer-managed policy name.
func recordIamPolicy(name, id string) string {
	return `<member><PolicyName>` + name + `</PolicyName><PolicyId>` + id + `</PolicyId><Arn>` + recordIamCustomer + name + `</Arn>
		<PolicyVersionList><member><Document>` + recordIamDocument + `</Document><VersionId>v1</VersionId><IsDefaultVersion>true</IsDefaultVersion></member></PolicyVersionList></member>`
}

// recordIamInline is the list key of inline policies called names.
func recordIamInline(key string, names ...string) string {
	members := ""
	for _, name := range names {
		members += `<member><PolicyName>` + name + `</PolicyName><PolicyDocument>` + recordIamDocument + `</PolicyDocument></member>`
	}
	return `<` + key + `>` + members + `</` + key + `>`
}

// recordIamAttached is the list of attached managed policies made of
// policies, each from recordIamAttachment.
func recordIamAttached(policies ...string) string {
	members := ""
	for _, policy := range policies {
		members += policy
	}
	return `<AttachedManagedPolicies>` + members + `</AttachedManagedPolicies>`
}

// recordIamAttachment is one attached managed policy.
func recordIamAttachment(name, arn string) string {
	return `<member><PolicyName>` + name + `</PolicyName><PolicyArn>` + arn + `</PolicyArn></member>`
}

// The lister reads the account's authorization details, across pages, and
// records from them: users, each followed by its inline policies, policy
// attachments, group memberships and access keys, the one list read user
// by user; groups, each followed by its inline policies and policy
// attachments; customer-managed policies; roles, each followed by its
// inline policies and policy attachments. It then lists instance profiles.
// A page mixes users, groups, policies and roles, and the lister still
// records them kind by kind. A user deleted before its access keys are
// listed has them left out. No user, group or role is asked for its own
// policies or groups.
func TestRecordIamListsUsersGroupsPoliciesRolesAndProfiles(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		user := form.Get("UserName")
		switch {
		case call.Op == "GetAccountAuthorizationDetails":
			filter := []string{form.Get("Filter.member.1"), form.Get("Filter.member.2"), form.Get("Filter.member.3"), form.Get("Filter.member.4"), form.Get("Filter.member.5")}
			if want := []string{"User", "Group", "LocalManagedPolicy", "Role", ""}; !slices.Equal(filter, want) {
				t.Errorf("authorization details filtered by %q, want %q", filter, want)
			}
			return recordIamDetails(call,
				`<UserDetailList>`+recordIamUser("alice@example.com", "AIDAALICE",
					recordIamInline("UserPolicyList", "s3-inline", "kms-inline")+
						`<GroupList><member>admins</member><member>devs</member></GroupList>`+
						recordIamAttached(
							recordIamAttachment("ReadOnlyAccess", recordIamAWS+"ReadOnlyAccess"),
							recordIamAttachment("custom", recordIamCustomer+"custom")))+`</UserDetailList>`+
					`<GroupDetailList>`+recordIamGroup("admins",
					recordIamInline("GroupPolicyList", "admin-inline", "audit-inline")+
						recordIamAttached(
							recordIamAttachment("AdministratorAccess", recordIamAWS+"AdministratorAccess"),
							recordIamAttachment("custom", recordIamCustomer+"custom"),
							recordIamAttachment("IAMReadOnlyAccess", recordIamAWS+"IAMReadOnlyAccess")))+`</GroupDetailList>`+
					`<RoleDetailList>`+recordIamRole("app",
					`<AssumeRolePolicyDocument>`+recordIamDocument+`</AssumeRolePolicyDocument>`+
						recordIamInline("RolePolicyList", "inline-sqs", "inline-sns")+
						recordIamAttached(
							recordIamAttachment("custom", recordIamCustomer+"custom"),
							recordIamAttachment("AmazonS3ReadOnlyAccess", recordIamAWS+"AmazonS3ReadOnlyAccess")))+`</RoleDetailList>`+
					`<Policies>`+recordIamPolicy("custom", "ANPA1")+`</Policies>`,
				`<UserDetailList>`+recordIamUser("bob", "AIDABOB", "")+`</UserDetailList>`+
					`<GroupDetailList>`+recordIamGroup("devs", recordIamInline("GroupPolicyList")+recordIamAttached())+`</GroupDetailList>`+
					`<RoleDetailList>`+recordIamRole("AWSServiceRoleForSupport",
					recordIamAttached(recordIamAttachment("AWSSupportServiceRolePolicy", recordIamAWS+"aws-service-role/AWSSupportServiceRolePolicy")))+`</RoleDetailList>`+
					`<Policies>`+recordIamPolicy("boundary", "ANPA2")+`</Policies>`)

		case call.Op == "ListAccessKeys" && user == "alice@example.com":
			return recordIamList(call, "AccessKeyMetadata",
				`<member><UserName>alice@example.com</UserName><AccessKeyId>AKIAALICE1</AccessKeyId><Status>Active</Status></member>`,
				`<member><UserName>alice@example.com</UserName><AccessKeyId>AKIAALICE2</AccessKeyId><Status>Inactive</Status></member>`)
		case call.Op == "ListAccessKeys" && user == "bob":
			return recordQueryError(recordIamNS, "NoSuchEntity")

		case call.Op == "ListInstanceProfiles":
			return recordIamList(call, "InstanceProfiles",
				`<member><InstanceProfileName>app-profile</InstanceProfileName><InstanceProfileId>AIPA1</InstanceProfileId><Path>/</Path>
					<Arn>arn:aws:iam::123456789012:instance-profile/app-profile</Arn><Roles>`+recordIamRole("app", "")+`</Roles></member>`,
				`<member><InstanceProfileName>batch-profile</InstanceProfileName><InstanceProfileId>AIPA2</InstanceProfileId><Path>/</Path>
					<Arn>arn:aws:iam::123456789012:instance-profile/batch-profile</Arn><Roles/></member>`)
		}
		// ListUsers, ListUserPolicies, ListGroupsForUser, ListGroups,
		// ListRolePolicies and the like are no longer asked: the
		// authorization details hold all they would answer.
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

// Every list's error is returned, a user's access keys included: a failed
// list must not pass for an empty one.
func TestRecordIamReturnsErrors(t *testing.T) {
	for _, failing := range []string{"GetAccountAuthorizationDetails", "ListAccessKeys", "ListInstanceProfiles"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordQueryError(recordIamNS, "AccessDenied")
				case "GetAccountAuthorizationDetails":
					return recordIamDetails(call, `<UserDetailList>`+recordIamUser("alice", "AIDAALICE", "")+`</UserDetailList>`+
						`<GroupDetailList>`+recordIamGroup("admins", "")+`</GroupDetailList>`+
						`<RoleDetailList>`+recordIamRole("app", "")+`</RoleDetailList>`, "")
				case "ListAccessKeys":
					return recordIamList(call, "AccessKeyMetadata", "", "")
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

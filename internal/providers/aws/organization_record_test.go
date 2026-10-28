// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"
)

const recordOrgARN = "arn:aws:organizations::111111111111:"

// recordOrgAPI answers the Organizations lister for the caller account
// caller. The organization o-1 is managed by 111111111111. The tree: root
// r-1 holds accounts 111111111111 and 222222222222, then 444444444444 on a
// second page, and OU ou-1; ou-1 holds account 333333333333 and OU ou-2,
// which holds OU ou-3. Service control policies: the AWS managed
// FullAWSAccess on r-1, then, on a second page, deny-root on ou-1 and
// 222222222222 (the latter on a second page of targets). The operation
// failing answers with an error.
func recordOrgAPI(t *testing.T, caller, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			if call.Op == "GetCallerIdentity" {
				return recordQueryError(recordSTSNS, "AccessDenied")
			}
			return recordJSONError("AccessDeniedException")
		}
		parent := recordJSONField(call, "ParentId")
		switch call.Op {
		case "GetCallerIdentity":
			return recordQuery(call.Op, recordSTSNS,
				`<Arn>arn:aws:iam::`+caller+`:user/lister</Arn><UserId>AIDA1</UserId><Account>`+caller+`</Account>`)
		case "DescribeOrganization":
			return `{"Organization":{"Id":"o-1","Arn":"` + recordOrgARN + `organization/o-1","FeatureSet":"ALL",
				"MasterAccountId":"111111111111","MasterAccountArn":"` + recordOrgARN + `account/o-1/111111111111"}}`
		case "ListRoots":
			return `{"Roots":[{"Id":"r-1","Arn":"` + recordOrgARN + `root/o-1/r-1","Name":"Root"}]}`
		case "ListAccountsForParent":
			switch parent {
			case "r-1":
				if recordSecondPage(call) {
					return `{"Accounts":[{"Id":"444444444444","Arn":"` + recordOrgARN + `account/o-1/444444444444","Name":"second-page"}]}`
				}
				return `{"Accounts":[
					{"Id":"111111111111","Arn":"` + recordOrgARN + `account/o-1/111111111111","Name":"management","Status":"ACTIVE"},
					{"Id":"222222222222","Arn":"` + recordOrgARN + `account/o-1/222222222222","Name":"prod","Status":"ACTIVE"}],
					"NextToken":"` + nextPageToken + `"}`
			case "ou-1":
				return `{"Accounts":[{"Id":"333333333333","Arn":"` + recordOrgARN + `account/o-1/333333333333","Name":"staging"}]}`
			case "ou-2", "ou-3":
				return `{"Accounts":[]}`
			}
		case "ListOrganizationalUnitsForParent":
			switch parent {
			case "r-1":
				return `{"OrganizationalUnits":[{"Id":"ou-1","Arn":"` + recordOrgARN + `ou/o-1/ou-1","Name":"Workloads"}]}`
			case "ou-1":
				return `{"OrganizationalUnits":[{"Id":"ou-2","Arn":"` + recordOrgARN + `ou/o-1/ou-2","Name":"Sandbox"}]}`
			case "ou-2":
				return `{"OrganizationalUnits":[{"Id":"ou-3","Arn":"` + recordOrgARN + `ou/o-1/ou-3","Name":"Experiments"}]}`
			case "ou-3":
				return `{"OrganizationalUnits":[]}`
			}
		case "ListPolicies":
			if filter := recordJSONField(call, "Filter"); filter != "SERVICE_CONTROL_POLICY" {
				t.Errorf("ListPolicies of %q", filter)
			}
			if recordSecondPage(call) {
				return `{"Policies":[
					{"Id":"p-deny","Arn":"` + recordOrgARN + `policy/o-1/service_control_policy/p-deny","Name":"deny-root","Type":"SERVICE_CONTROL_POLICY","AwsManaged":false}]}`
			}
			return `{"Policies":[{"Id":"p-FullAWSAccess","Arn":"arn:aws:organizations::aws:policy/service_control_policy/p-FullAWSAccess","Name":"FullAWSAccess","Type":"SERVICE_CONTROL_POLICY","AwsManaged":true}],
				"NextToken":"` + nextPageToken + `"}`
		case "ListTargetsForPolicy":
			switch recordJSONField(call, "PolicyId") {
			case "p-FullAWSAccess":
				return `{"Targets":[{"TargetId":"r-1","Name":"Root","Type":"ROOT"}]}`
			case "p-deny":
				if recordSecondPage(call) {
					return `{"Targets":[{"TargetId":"222222222222","Name":"prod","Type":"ACCOUNT"}]}`
				}
				return `{"Targets":[{"TargetId":"ou-1","Name":"Workloads","Type":"ORGANIZATIONAL_UNIT"}],"NextToken":"` + nextPageToken + `"}`
			}
		}
		return recordUnexpected(t, call)
	}
}

// recordOrgTree is what the lister records of recordOrgAPI's organization
// after the organization itself.
func recordOrgTree() []recordedResource {
	account := func(id, name string) recordedResource {
		return recordedResource{"aws_organizations_account", id, name, map[string]string{"id": id, "arn": recordOrgARN + "account/o-1/" + id}}
	}
	ou := func(id, name string) recordedResource {
		return recordedResource{"aws_organizations_organizational_unit", id, name, map[string]string{"id": id, "arn": recordOrgARN + "ou/o-1/" + id}}
	}
	policy := func(id, name, arn, awsManaged string) recordedResource {
		return recordedResource{"aws_organizations_policy", id, name, map[string]string{"id": id, "arn": arn, "aws_managed": awsManaged}}
	}
	attachment := func(target, policyID, policyName, awsManaged string) recordedResource {
		return recordedResource{"aws_organizations_policy_attachment", target + ":" + policyID, "pa-" + target + ":" + policyName,
			map[string]string{"policy_id": policyID, "target_id": target, "policy_aws_managed": awsManaged}}
	}
	return []recordedResource{
		account("111111111111", "management"),
		account("222222222222", "prod"),
		account("444444444444", "second-page"),
		ou("ou-1", "Workloads"),
		account("333333333333", "staging"),
		ou("ou-2", "Sandbox"),
		ou("ou-3", "Experiments"),
		// The catalogue leaves the AWS managed FullAWSAccess and its
		// attachments out of the default selection.
		policy("p-FullAWSAccess", "FullAWSAccess", "arn:aws:organizations::aws:policy/service_control_policy/p-FullAWSAccess", "true"),
		attachment("r-1", "p-FullAWSAccess", "FullAWSAccess", "true"),
		policy("p-deny", "deny-root", recordOrgARN+"policy/o-1/service_control_policy/p-deny", "false"),
		attachment("ou-1", "p-deny", "deny-root", "false"),
		attachment("222222222222", "p-deny", "deny-root", "false"),
	}
}

// From the management account, the lister records the organization once,
// by its ID, then walks the tree from each root: a parent's accounts, then
// its OUs, each followed by what it holds; then the service control
// policies, each followed by its attachments. Every list is read across
// pages.
func TestRecordOrganizationListsTreeAndPolicies(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordOrgAPI(t, "111111111111", ""))
	g := &OrganizationLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources, append([]recordedResource{
		{"aws_organizations_organization", "o-1", "o-1", map[string]string{"id": "o-1", "arn": recordOrgARN + "organization/o-1"}},
	}, recordOrgTree()...)...)
}

// A delegated administrator can list the tree, but only the management
// account manages the organization, so it isn't recorded.
func TestRecordOrganizationLeavesTheOrganizationToItsManagementAccount(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordOrgAPI(t, "222222222222", ""))
	g := &OrganizationLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources, recordOrgTree()...)
}

// Every error is returned, the walk's included: a part of the tree left
// out would leave out everything below it.
func TestRecordOrganizationReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{
		"DescribeOrganization", "GetCallerIdentity", "ListRoots", "ListAccountsForParent",
		"ListOrganizationalUnitsForParent", "ListPolicies", "ListTargetsForPolicy",
	} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordOrgAPI(t, "111111111111", failing))
			g := &OrganizationLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}

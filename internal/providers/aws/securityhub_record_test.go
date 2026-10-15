// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

const (
	recordSTSNS              = "https://sts.amazonaws.com/doc/2011-06-15/"
	recordSecurityHubSubARN  = "arn:aws:securityhub:us-east-1:123456789012:subscription/"
	recordSecurityHubStdsARN = "arn:aws:securityhub:us-east-1::standards/"
)

func recordSecurityHubCallerIdentity() string {
	return recordQuery("GetCallerIdentity", recordSTSNS,
		`<Arn>arn:aws:iam::123456789012:user/lister</Arn><UserId>AIDA1</UserId><Account>123456789012</Account>`)
}

// recordSecurityHubAPI answers the Security Hub lister: two pages of
// enabled standards and two pages of members. Answers in override win, by
// operation (GetCallerIdentity) or path.
func recordSecurityHubAPI(t *testing.T, override map[string]string) func(apiCall) string {
	return func(call apiCall) string {
		key := call.Op
		if key == "" {
			key = call.Path
		}
		if answer, ok := override[key]; ok {
			return answer
		}
		switch key {
		case "GetCallerIdentity":
			return recordSecurityHubCallerIdentity()
		case "/standards/get":
			if recordSecondPage(call) {
				return `{"StandardsSubscriptions":[{"StandardsSubscriptionArn":"` + recordSecurityHubSubARN + `cis-aws-foundations-benchmark/v/1.4.0",
					"StandardsArn":"` + recordSecurityHubStdsARN + `cis-aws-foundations-benchmark/v/1.4.0","StandardsStatus":"READY","StandardsInput":{}}]}`
			}
			return `{"StandardsSubscriptions":[{"StandardsSubscriptionArn":"` + recordSecurityHubSubARN + `aws-foundational-security-best-practices/v/1.0.0",
				"StandardsArn":"` + recordSecurityHubStdsARN + `aws-foundational-security-best-practices/v/1.0.0","StandardsStatus":"READY","StandardsInput":{}}],
				"NextToken":"` + nextPageToken + `"}`
		case "/members":
			if recordSecondPage(call) {
				return `{"Members":[{"AccountId":"333333333333","MemberStatus":"Enabled"}]}`
			}
			return `{"Members":[{"AccountId":"222222222222","Email":"security@example.com","MemberStatus":"Enabled"}],"NextToken":"` + nextPageToken + `"}`
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists the account (by ID, from STS) when Security Hub is
// enabled, its members by account ID, and its standards subscriptions by
// ARN, across pages.
func TestRecordSecurityHubListsAccountMembersAndStandards(t *testing.T) {
	useFakeAPI(t, recordSecurityHubAPI(t, nil))
	g := &SecurityhubLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	fsbp := recordSecurityHubSubARN + "aws-foundational-security-best-practices/v/1.0.0"
	cis := recordSecurityHubSubARN + "cis-aws-foundations-benchmark/v/1.4.0"
	assertRecorded(t, g.Resources,
		recordedResource{"aws_securityhub_account", "123456789012", "123456789012", nil},
		recordedResource{"aws_securityhub_member", "222222222222", "securityhub_member_222222222222", map[string]string{
			"account_id": "222222222222", "email": "security@example.com"}},
		recordedResource{"aws_securityhub_member", "333333333333", "securityhub_member_333333333333", map[string]string{
			"account_id": "333333333333"}},
		// Imported by the subscription's ARN; standards_arn is the standard's.
		recordedResource{"aws_securityhub_standards_subscription", fsbp, fsbp, map[string]string{
			"standards_arn": recordSecurityHubStdsARN + "aws-foundational-security-best-practices/v/1.0.0"}},
		recordedResource{"aws_securityhub_standards_subscription", cis, cis, map[string]string{
			"standards_arn": recordSecurityHubStdsARN + "cis-aws-foundations-benchmark/v/1.4.0"}},
	)
}

// An account that hasn't enabled Security Hub has nothing to list, and that
// is not an error.
func TestRecordSecurityHubNotSubscribed(t *testing.T) {
	useFakeAPI(t, recordSecurityHubAPI(t, map[string]string{
		"/standards/get": `{"__type":"InvalidAccessException","Code":"InvalidAccessException","Message":"Account 123456789012 is not subscribed to AWS Security Hub"}`,
		// Members aren't listed: this would fail the service.
		"/members": recordJSONError("UnexpectedCall"),
	}))
	g := &SecurityhubLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources)
}

func TestRecordSecurityHubReturnsErrors(t *testing.T) {
	for key, answer := range map[string]string{
		"GetCallerIdentity": recordQueryError(recordSTSNS, "AccessDenied"),
		"/standards/get":    recordJSONError("AccessDenied"),
		"/members":          recordJSONError("AccessDenied"),
	} {
		t.Run(key, func(t *testing.T) {
			useFakeAPI(t, recordSecurityHubAPI(t, map[string]string{key: answer}))
			g := &SecurityhubLister{}

			err := g.List()

			assertRecordedError(t, err, "AccessDenied")
		})
	}
}

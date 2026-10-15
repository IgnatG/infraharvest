// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

func recordCognitoAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			return recordJSONError("NotAuthorizedException")
		}
		// The three lists require MaxResults.
		if !strings.Contains(call.Body, `"MaxResults":`) {
			t.Errorf("%s without MaxResults: %s", call.Op, call.Body)
		}
		second := recordSecondPage(call)
		next := `,"NextToken":"` + nextPageToken + `"`
		switch call.Op {
		case "ListIdentityPools":
			if second {
				return `{"IdentityPools":[{"IdentityPoolId":"us-east-1:2222","IdentityPoolName":"partners"}]}`
			}
			return `{"IdentityPools":[{"IdentityPoolId":"us-east-1:1111","IdentityPoolName":"customers"}]` + next + `}`
		case "ListUserPools":
			if second {
				return `{"UserPools":[{"Id":"us-east-1_BBB","Name":"staff","Status":"Enabled"}]}`
			}
			return `{"UserPools":[{"Id":"us-east-1_AAA","Name":"customers","Status":"Enabled"}]` + next + `}`
		case "ListUserPoolClients":
			switch recordJSONField(call, "UserPoolId") {
			case "us-east-1_AAA":
				if second {
					return `{"UserPoolClients":[{"ClientId":"c2","ClientName":"mobile","UserPoolId":"us-east-1_AAA"}]}`
				}
				return `{"UserPoolClients":[{"ClientId":"c1","ClientName":"web","UserPoolId":"us-east-1_AAA"}]` + next + `}`
			case "us-east-1_BBB":
				return `{"UserPoolClients":[]}`
			}
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists identity pools, then user pools, then each user pool's
// clients, by ID, named <name>_<ID>, across pages.
func TestRecordCognitoListsPoolsAndClients(t *testing.T) {
	useFakeAPI(t, recordCognitoAPI(t, ""))
	g := &CognitoLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_cognito_identity_pool", "us-east-1:1111", "customers_us-east-1:1111", nil},
		recordedResource{"aws_cognito_identity_pool", "us-east-1:2222", "partners_us-east-1:2222", nil},
		recordedResource{"aws_cognito_user_pool", "us-east-1_AAA", "customers_us-east-1_AAA", nil},
		recordedResource{"aws_cognito_user_pool", "us-east-1_BBB", "staff_us-east-1_BBB", nil},
		recordedResource{"aws_cognito_user_pool_client", "c1", "web_c1", map[string]string{"user_pool_id": "us-east-1_AAA"}},
		recordedResource{"aws_cognito_user_pool_client", "c2", "mobile_c2", map[string]string{"user_pool_id": "us-east-1_AAA"}},
	)
}

func TestRecordCognitoReturnsErrors(t *testing.T) {
	for _, failing := range []string{"ListIdentityPools", "ListUserPools", "ListUserPoolClients"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, recordCognitoAPI(t, failing))
			g := &CognitoLister{}

			assertRecordedError(t, g.List(), "NotAuthorizedException")
		})
	}
}

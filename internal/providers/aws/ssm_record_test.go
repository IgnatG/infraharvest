// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists every parameter by name, across pages, whatever its
// type: SecureString parameters are listed too.
func TestRecordSsmListsParameters(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeParameters" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"Parameters":[{"Name":"/app/db/password","Type":"SecureString","KeyId":"alias/aws/ssm","Version":3}]}`
		}
		return fmt.Sprintf(`{"Parameters":[
			{"Name":"/app/db/host","Type":"String","Version":1,"Tier":"Standard","DataType":"text"},
			{"Name":"feature-flags","Type":"StringList","Version":2}],"NextToken":%q}`, nextPageToken)
	})
	g := &SsmLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_ssm_parameter", "/app/db/host", "/app/db/host", nil},
		recordedResource{"aws_ssm_parameter", "feature-flags", "feature-flags", nil},
		recordedResource{"aws_ssm_parameter", "/app/db/password", "/app/db/password", nil},
	)
}

func TestRecordSsmReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &SsmLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "AccessDeniedException")
}

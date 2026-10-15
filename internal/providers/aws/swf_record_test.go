// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists the registered domains by name, across pages. It
// doesn't ask for the deprecated ones.
func TestRecordSwfListsRegisteredDomains(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListDomains" {
			return recordUnexpected(t, call)
		}
		if status := recordJSONField(call, "registrationStatus"); status != "REGISTERED" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"domainInfos":[{"name":"billing","status":"REGISTERED"}]}`
		}
		return `{"domainInfos":[{"name":"orders","status":"REGISTERED","arn":"arn:aws:swf:us-east-1:123456789012:/domain/orders"}],"nextPageToken":"` + nextPageToken + `"}`
	})
	g := &SWFLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_swf_domain", "orders", "orders", nil},
		recordedResource{"aws_swf_domain", "billing", "billing", nil},
	)
}

func TestRecordSwfReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("OperationNotPermittedFault")
	})
	g := &SWFLister{}

	assertRecordedError(t, g.List(), "OperationNotPermittedFault")
}

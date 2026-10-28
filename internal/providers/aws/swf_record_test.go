// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists the registered domains by name, across pages. It
// doesn't ask for the deprecated ones.
func TestRecordSwfListsRegisteredDomains(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
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

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_swf_domain", "orders", "orders", nil},
		recordedResource{"aws_swf_domain", "billing", "billing", nil},
	)
}

func TestRecordSwfReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("OperationNotPermittedFault")
	})
	g := &SWFLister{}

	assertRecordedError(t, listError(ctx, g), "OperationNotPermittedFault")
}

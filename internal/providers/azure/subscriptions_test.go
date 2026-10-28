// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
)

func TestSubscriptionsIn(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{body: `{"totalRecords":4,"count":4,"resultTruncated":"false","data":[
		{"subscriptionId":"sub-b","state":"Enabled"},
		{"subscriptionId":"sub-a","state":"Warned"},
		{"subscriptionId":"sub-off","state":"Disabled"},
		{"subscriptionId":"sub-b","state":"Enabled"}
	]}`}
	options := &arm.ClientOptions{Transport: transport}
	got, err := subscriptionsIn(t.Context(), fakeCredential{}, options, "platform")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sub-a", "sub-b"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(transport.paths) != 1 || !strings.HasSuffix(transport.paths[0], "/providers/Microsoft.ResourceGraph/resources") {
		t.Errorf("requests: %v", transport.paths)
	}
	if _, err := subscriptionsIn(t.Context(), fakeCredential{}, options, "mg' or '1"); err == nil {
		t.Error("a management group ID with a quote: no error")
	}
}

func TestInitNeedsASubscription(t *testing.T) {
	t.Setenv("ARM_SUBSCRIPTION_ID", "")
	if err := (&Provider{}).Init(t.Context(), []string{""}); err != errNoSubscription {
		t.Errorf("got %v, want %v", err, errNoSubscription)
	}
}

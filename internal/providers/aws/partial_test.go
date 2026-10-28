// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A part that failed makes the listing partial, naming the part; one whose
// resource is gone, or whose operation the endpoint doesn't offer, leaves
// nothing to list and is no failure; without failures, there is no error.
func TestPartFailures(t *testing.T) {
	t.Parallel()
	var none partFailures
	none.add("nothing", nil)
	if err := none.err(); err != nil {
		t.Errorf("without failures: %v", err)
	}

	var parts partFailures
	parts.add("listeners of lb-1", &smithy.GenericAPIError{Code: "LoadBalancerNotFound"})
	parts.add("policy of bucket b", &smithy.GenericAPIError{Code: "NoSuchBucket"})
	parts.add("grants of key k", &smithy.GenericAPIError{Code: "NotImplemented"})
	parts.add("grants of key k-2", &smithy.GenericAPIError{Code: "UnknownOperationException"})
	parts.add("rules of listener l-1", &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"})

	err := parts.err()
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "rules of listener l-1") {
		t.Fatalf("want a partial error for the rules, got %v", err)
	}
	for _, skipped := range []string{"lb-1", "bucket b", "key k:", "key k-2"} {
		if strings.Contains(err.Error(), skipped) {
			t.Errorf("%s is no failure: %v", skipped, err)
		}
	}
}

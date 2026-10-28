// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A NotFound or NoSuch code is a resource gone; an operation the endpoint
// doesn't implement leaves nothing to list; an endpoint the region
// doesn't have, or an operation AWS says is unavailable, skips the
// region; anything else, AccessDenied, throttling and a DNS failure that
// may pass included, is a failure.
func TestAWSCodes(t *testing.T) {
	t.Parallel()
	apiError := func(code, message string) error {
		return fmt.Errorf("operation error: %w", &smithy.GenericAPIError{Code: code, Message: message})
	}
	for _, tc := range []struct {
		name string
		err  error
		want harvest.Outcome
	}{
		{"NotFound", apiError("LoadBalancerNotFound", ""), harvest.OutcomeGone},
		{"NotFoundException", apiError("RepositoryNotFoundException", ""), harvest.OutcomeGone},
		{"NoSuch", apiError("NoSuchBucket", ""), harvest.OutcomeGone},
		{"NotImplemented", apiError("NotImplemented", ""), harvest.OutcomeNothingToList},
		{"UnknownOperationException", apiError("UnknownOperationException", ""), harvest.OutcomeNothingToList},
		{"UnsupportedOperation", apiError("UnsupportedOperation", ""), harvest.OutcomeNothingToList},
		{"InvalidAction", apiError("InvalidAction", ""), harvest.OutcomeNothingToList},
		{"UnavailableOperation", apiError("UnavailableOperation", "not available"), harvest.OutcomeSkipRegion},
		{"unavailable in the message", apiError("InvalidRequest", "Unavailable Operation"), harvest.OutcomeSkipRegion},
		{"no such host", &net.DNSError{Err: "no such host", Name: "svc.eu-south-2.amazonaws.com", IsNotFound: true}, harvest.OutcomeSkipRegion},
		{"another host's certificate", x509.HostnameError{Host: "svc.eu-south-2.amazonaws.com"}, harvest.OutcomeSkipRegion},
		{"another host's certificate, by pointer", &x509.HostnameError{Host: "svc.eu-south-2.amazonaws.com"}, harvest.OutcomeSkipRegion},
		{"DNS failure that may pass", &net.DNSError{Err: "server misbehaving", IsTemporary: true}, harvest.OutcomeFail},
		{"AccessDenied", apiError("AccessDenied", "denied"), harvest.OutcomeFail},
		{"throttled", apiError("ThrottlingException", ""), harvest.OutcomeFail},
		{"no code", errors.New("connection reset"), harvest.OutcomeFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := awsCodes.Classify(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The parts of a listing the AWS table says leave nothing out are no
// failure; the others make it partial, naming the part.
func TestAWSParts(t *testing.T) {
	t.Parallel()
	var parts harvest.Parts
	parts.Add(awsCodes, "listeners of lb-1", &smithy.GenericAPIError{Code: "LoadBalancerNotFound"})
	parts.Add(awsCodes, "grants of key k", &smithy.GenericAPIError{Code: "NotImplemented"})
	parts.Add(awsCodes, "rules of listener l-1", &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"})
	err := parts.Err()
	if !harvest.IsPartial(err) || err.Error() != "rules of listener l-1: api error AccessDenied: denied" {
		t.Errorf("want a partial error for the rules alone, got %v", err)
	}
}

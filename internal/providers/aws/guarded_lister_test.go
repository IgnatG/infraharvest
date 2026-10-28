// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// failingService is a service whose listing fails with err.
type failingService struct {
	err error
}

func (f *failingService) List(context.Context, harvest.Settings) ([]harvest.Resource, error) {
	return nil, f.err
}

// A service AWS doesn't offer in the region is skipped; every other error,
// timeouts included, is returned.
func TestGuardedListerSkipsOnlyServicesUnavailableInTheRegion(t *testing.T) {
	t.Parallel()
	sendErr := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://svc.eu-south-2.amazonaws.com/", Err: err}
	}
	for name, tc := range map[string]struct {
		err  error
		skip bool
	}{
		"no endpoint": {sendErr(&net.DNSError{Err: "no such host", Name: "svc.eu-south-2.amazonaws.com", IsNotFound: true}), true},
		"other host's certificate": {sendErr(&tls.CertificateVerificationError{
			Err: x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{"*.amazonaws.com"}}, Host: "svc.eu-south-2.amazonaws.com"},
		}), true},
		"unavailable operation": {fmt.Errorf("operation error: %w", &smithy.GenericAPIError{Code: "UnavailableOperation", Message: "not available"}), true},
		"timeout":               {sendErr(&net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}), false},
		"DNS failure":           {sendErr(&net.DNSError{Err: "server misbehaving", Name: "svc.eu-south-2.amazonaws.com", IsTemporary: true}), false},
		"access denied":         {&smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not allowed"}, false},
		"other":                 {errors.New("boom"), false},
	} {
		t.Run(name, func(t *testing.T) {
			guard := &guardedLister{lister: &failingService{err: tc.err}}

			_, err := guard.List(t.Context(), harvest.Settings{Service: "sqs"})

			if tc.skip && err != nil {
				t.Errorf("want the service skipped, got %v", err)
			}
			if !tc.skip && !errors.Is(err, tc.err) {
				t.Errorf("want %v returned, got %v", tc.err, err)
			}
		})
	}
}

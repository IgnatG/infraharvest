// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The codes awsCodes gives answers that carry no AWS error code of their
// own, that the region doesn't offer the service.
const (
	// codeNoSuchHost is a DNS answer that the endpoint's host doesn't
	// exist: AWS has no endpoint for the service in the region.
	codeNoSuchHost = "dns: no such host"
	// codeHostnameMismatch is a TLS certificate of another host than the
	// endpoint's: its name is served by a host that isn't the service's.
	codeHostnameMismatch = "x509: certificate of another host"
	// codeUnavailableOperation is the code of an API that says the
	// operation is unavailable, in its code or, where it gives no such
	// code, in its message.
	codeUnavailableOperation = "UnavailableOperation"
)

// awsCodes is what AWS's answers mean for a listing (see harvest.Codes):
//
//   - The resource is gone: an error code ending in NotFound or
//     NotFoundException, such as LoadBalancerNotFound, or starting with
//     NoSuch, such as NoSuchBucket.
//   - Nothing to list: the endpoint doesn't implement the operation, as an
//     emulator may not, as for S3's NotImplemented.
//   - The region doesn't offer the service: it has no endpoint there, the
//     endpoint's name is served by another host's certificate, or the API
//     says the operation is unavailable. A timeout or a DNS failure that
//     may pass is not that.
//
// Any other answer, AccessDenied or throttling included, is a failure.
var awsCodes = &harvest.Codes{
	Of: awsErrorCodes,
	Exact: map[string]harvest.Outcome{
		"NotImplemented":            harvest.OutcomeNothingToList,
		"UnknownOperationException": harvest.OutcomeNothingToList,
		"UnsupportedOperation":      harvest.OutcomeNothingToList,
		"InvalidAction":             harvest.OutcomeNothingToList,
		codeUnavailableOperation:    harvest.OutcomeSkipRegion,
		codeNoSuchHost:              harvest.OutcomeSkipRegion,
		codeHostnameMismatch:        harvest.OutcomeSkipRegion,
	},
	Patterns: []harvest.CodePattern{
		{Pattern: "*NotFound", Outcome: harvest.OutcomeGone},
		{Pattern: "*NotFoundException", Outcome: harvest.OutcomeGone},
		{Pattern: "NoSuch*", Outcome: harvest.OutcomeGone},
	},
}

// awsErrorCodes returns the codes of err for awsCodes: a DNS answer that
// the host doesn't exist, or a certificate of another host, by their own
// codes; an AWS API error's code, after codeUnavailableOperation when its
// message says the operation is unavailable: that is matched by text, as
// some APIs give no code of their own for it, and comes first so that it
// skips the region whatever code the API gives with it.
func awsErrorCodes(err error) []string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return []string{codeNoSuchHost}
	}
	var hostErr x509.HostnameError
	var hostErrPtr *x509.HostnameError
	if errors.As(err, &hostErr) || errors.As(err, &hostErrPtr) {
		return []string{codeHostnameMismatch}
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return nil
	}
	if strings.Contains(apiErr.ErrorMessage(), "Unavailable Operation") {
		return []string{codeUnavailableOperation, apiErr.ErrorCode()}
	}
	return []string{apiErr.ErrorCode()}
}

// apiErrorCode returns the error code of an AWS API error, "" for any
// other error.
func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

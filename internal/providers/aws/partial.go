// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"
	"strings"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// partFailures gathers the errors of the parts of a listing that failed,
// such as a load balancer's listeners or a key's grants, while the rest is
// still listed. Returned by err, they make the listing read as incomplete
// (see harvest.Partial), so that what was listed is imported and the run
// says what is missing, rather than reading as a listing in full.
type partFailures struct {
	errs []error
}

// add records err, the failure of the part what, unless it says the
// resource is gone, deleted since it was listed, or that the endpoint
// doesn't offer the operation (see operationUnsupported): either leaves
// nothing to list. A nil err is no failure.
func (p *partFailures) add(what string, err error) {
	if err == nil || resourceGone(err) || operationUnsupported(err) {
		return
	}
	p.errs = append(p.errs, fmt.Errorf("%s: %w", what, err))
}

// unsupportedOperationCodes are the error codes an endpoint answers an
// operation it doesn't implement with, as an emulator may: there is then
// nothing of that kind to import, as for S3's NotImplemented.
var unsupportedOperationCodes = map[string]bool{
	"NotImplemented":            true,
	"UnknownOperationException": true,
	"UnsupportedOperation":      true,
	"InvalidAction":             true,
}

// operationUnsupported reports whether err says the endpoint doesn't
// implement the operation asked (see unsupportedOperationCodes).
func operationUnsupported(err error) bool {
	return unsupportedOperationCodes[apiErrorCode(err)]
}

// err returns the failures as one partial error, or nil when there are
// none.
func (p *partFailures) err() error {
	return harvest.Partial(errors.Join(p.errs...))
}

// resourceGone reports whether err is an AWS answer that the resource asked
// about doesn't exist: an error code ending in NotFound or
// NotFoundException, such as LoadBalancerNotFound, or starting with
// NoSuch, such as NoSuchBucket.
func resourceGone(err error) bool {
	code := apiErrorCode(err)
	return strings.HasSuffix(code, "NotFound") || strings.HasSuffix(code, "NotFoundException") || strings.HasPrefix(code, "NoSuch")
}

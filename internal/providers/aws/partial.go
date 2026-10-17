// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
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
// resource is gone, deleted since it was listed, which leaves nothing to
// list. A nil err is no failure.
func (p *partFailures) add(what string, err error) {
	if err == nil || resourceGone(err) {
		return
	}
	p.errs = append(p.errs, fmt.Errorf("%s: %w", what, err))
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

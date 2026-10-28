// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"errors"
	"fmt"
)

// PartialError is the error of a lister that listed its service, but not
// all of it, such as one whose list of a part was refused: what it listed
// is imported, and the error is a failure of the import all the same.
type PartialError struct {
	Err error
}

// Error returns the message of the error that left resources out.
func (e *PartialError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the error that left resources out.
func (e *PartialError) Unwrap() error {
	return e.Err
}

// Partial returns err as a PartialError, or nil when err is nil. A lister
// whose parts fail one by one gathers them in Parts, whose Err returns
// this.
func Partial(err error) error {
	if err == nil {
		return nil
	}
	return &PartialError{Err: err}
}

// IsPartial reports whether err says its lister listed in part: whether
// err is a PartialError or wraps one, through errors that wrap one error
// each, as fmt.Errorf with one %w does. An error that joins several, as
// errors.Join does, is partial only when it is itself wrapped by Partial,
// so that a lister's full failure joined to another's partial one still
// reads as a full failure.
func IsPartial(err error) bool {
	for err != nil {
		if _, ok := err.(*PartialError); ok {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Parts gathers the failures of the parts of a listing, such as a load
// balancer's listeners or an instance's databases, while the rest is still
// listed. Its Err makes the listing read as incomplete (see Partial), so
// that what was listed is imported and the run says what is missing,
// rather than reading as a listing in full. The zero value is ready to
// use. A Parts isn't safe for use by several goroutines at once: parts
// read in parallel each gather into their own, which Merge then joins in
// order.
type Parts struct {
	errs []error
}

// Add records err, the failure of the part what, as "what: err", or as
// err when what is "", and reports whether it recorded it. An error codes
// say leaves nothing out (see Outcome.LeavesNothingOut), as for a resource
// deleted since it was listed, is no failure and isn't recorded; with nil
// codes, every error is a failure. A nil err is no failure.
func (p *Parts) Add(codes *Codes, what string, err error) bool {
	if err == nil || codes.Classify(err).LeavesNothingOut() {
		return false
	}
	if what != "" {
		err = fmt.Errorf("%s: %w", what, err)
	}
	p.errs = append(p.errs, err)
	return true
}

// Merge records the failures of other after those of p.
func (p *Parts) Merge(other *Parts) {
	p.errs = append(p.errs, other.errs...)
}

// Err returns the failures joined as one partial error (see Partial), or
// nil when there are none.
func (p *Parts) Err() error {
	return Partial(errors.Join(p.errs...))
}

// ErrOutOf returns the failures of a listing of n parts: as the listing's
// failure when every one of the n failed, as nothing of it was listed;
// otherwise as Err does.
func (p *Parts) ErrOutOf(n int) error {
	if len(p.errs) > 0 && len(p.errs) == n {
		return errors.Join(p.errs...)
	}
	return p.Err()
}

// Fail returns err, which failed the whole listing, joined to the
// failures of the parts listed before it: the listing reads as failed,
// not partial (see IsPartial), and names every failure.
func (p *Parts) Fail(err error) error {
	return errors.Join(err, p.Err())
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

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

// Partial returns err as a PartialError, or nil when err is nil, so that
// a lister can return Partial(errors.Join(errs...)) for the parts it
// couldn't list.
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

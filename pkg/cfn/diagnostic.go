// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Severity says how much a diagnostic matters.
type Severity int

const (
	// Info is something the output does differently from CloudFormation,
	// or a name the mapper guessed, that needs no change to work.
	Info Severity = iota
	// Warning is something the converter carried over only partly, or left
	// for a person to finish, such as a property the mapper leaves out on
	// purpose, with the reason (an S3 bucket's encryption, which the AWS
	// provider manages with a resource of its own): the output may need
	// changing before it does what the template did.
	Warning
	// Error is something the converter couldn't convert at all, such as a
	// custom resource, a macro or an unknown function, or a property the
	// mapper has no argument for, or that maps to an argument another
	// property already set: what it says doesn't take effect. The output
	// leaves it out, or writes null in its place, with a comment that keeps
	// what the template said.
	Error
)

// String returns "info", "warning" or "error".
func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// Diagnostic is something Convert reports about a template. Nothing the
// converter can't carry over is left out silently: it is a diagnostic, and
// a comment in the output where it would have been.
type Diagnostic struct {
	// Severity says how much the diagnostic matters.
	Severity Severity
	// Path is where the diagnostic is in the template, as dotted keys and
	// list indexes, such as "Resources.Bucket.Properties.Tags[0].Value".
	// It is empty for the template as a whole.
	Path string
	// Message says what the converter did and, where a person has to act,
	// what to do.
	Message string
}

// String returns the diagnostic as "severity: path: message". The path
// and the message can hold the template's keys and strings: their control
// characters are written as Go escapes (see printable), so that printing
// a diagnostic can't send a terminal the escape sequences a template
// carries.
func (d Diagnostic) String() string {
	if d.Path == "" {
		return d.Severity.String() + ": " + printable(d.Message)
	}
	return d.Severity.String() + ": " + printable(d.Path) + ": " + printable(d.Message)
}

// printable returns s with each control character, such as a newline or
// the escape that starts a terminal's escape sequences, written as its Go
// escape, such as \n or \x1b, and each byte that isn't UTF-8 as the
// replacement character.
func printable(s string) string {
	if utf8Printable(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// utf8Printable reports whether s is valid UTF-8 without control
// characters: printable leaves it as it is.
func utf8Printable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// printableError is an error of Parse, whose text can hold the template's
// keys, with that text made printable (see printable); it unwraps to the
// error itself.
type printableError struct{ err error }

func (e printableError) Error() string { return printable(e.err.Error()) }

func (e printableError) Unwrap() error { return e.err }

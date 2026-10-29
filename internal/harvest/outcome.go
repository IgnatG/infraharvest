// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import "path"

// Outcome is what a failed call of a listing means for the listing: that
// it failed, that it listed in part, or that there was nothing to fail
// for, as the resource is gone, there is nothing of the kind to list, or
// the region doesn't offer the service. A provider's Codes decide it from
// the codes its cloud answers with (see Codes.Classify); the lister acts
// on it.
type Outcome int

const (
	// OutcomeFail is a call that left out what it should have listed,
	// such as one refused, throttled or failed by the cloud: the listing,
	// or the part of it the call was for, failed. It is the outcome of
	// any error the provider's codes don't name.
	OutcomeFail Outcome = iota
	// OutcomePartial is an error that already says its listing listed in
	// part (see Partial): what it listed is kept.
	OutcomePartial
	// OutcomeGone is an answer that the resource asked about doesn't
	// exist, as when it was deleted since it was listed: nothing that
	// exists is left out.
	OutcomeGone
	// OutcomeNothingToList is an answer that there is nothing of the kind
	// to list, such as an API the account hasn't enabled, an operation the
	// endpoint doesn't offer, or a child type a resource can't have.
	OutcomeNothingToList
	// OutcomeSkipRegion is an answer that the region doesn't offer the
	// service at all, such as an endpoint that doesn't exist there.
	OutcomeSkipRegion
)

// String names the outcome, for logs and tests.
func (o Outcome) String() string {
	switch o {
	case OutcomeFail:
		return "fail"
	case OutcomePartial:
		return "partial"
	case OutcomeGone:
		return "gone"
	case OutcomeNothingToList:
		return "nothing to list"
	case OutcomeSkipRegion:
		return "skip region"
	}
	return "unknown"
}

// LeavesNothingOut reports whether the outcome says the call left out
// nothing that could be imported: the resource is gone, or there is
// nothing of the kind to list.
func (o Outcome) LeavesNothingOut() bool {
	return o == OutcomeGone || o == OutcomeNothingToList
}

// Codes is a provider's table of the codes its cloud answers a call with,
// and what each means for the listing: such as AWS's error codes, Azure's
// HTTP statuses, or Google's API codes and reasons. Each provider keeps
// its own; harvest knows none of them.
type Codes struct {
	// Of returns the codes err carries, the most telling first, or none
	// for an error that carries no code the table names, such as a broken
	// connection. A provider may name answers without a code of their own,
	// such as a host its region doesn't have, with a code of its making.
	Of func(err error) []string
	// Exact gives the outcome of a code, before Patterns.
	Exact map[string]Outcome
	// Patterns give the outcome of the codes that match them, as
	// path.Match does, in order, for a code Exact doesn't name.
	Patterns []CodePattern
}

// CodePattern is a pattern of codes, as path.Match takes it, such as
// "*NotFound" or "5??", and the outcome of the codes it matches.
type CodePattern struct {
	Pattern string
	Outcome Outcome
}

// Classify returns what err, a failed call, means for the listing: a
// partial error (see IsPartial) is OutcomePartial; otherwise the first of
// the codes err carries that the table names decides; and any other error
// is OutcomeFail. A nil table names no code.
func (c *Codes) Classify(err error) Outcome {
	if IsPartial(err) {
		return OutcomePartial
	}
	if c == nil || c.Of == nil {
		return OutcomeFail
	}
	for _, code := range c.Of(err) {
		if outcome, ok := c.Exact[code]; ok {
			return outcome
		}
		for _, p := range c.Patterns {
			if ok, _ := path.Match(p.Pattern, code); ok {
				return p.Outcome
			}
		}
	}
	return OutcomeFail
}

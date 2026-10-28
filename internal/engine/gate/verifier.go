// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import "context"

// A Verifier settles a set of edits by rounds: each makes the edits still
// in play on the files as they were, verifies what they make, such as by
// a plan or the checks of the gate, and, if that fails, drops the edits
// the failure is blamed on, for the next round to make the others. The
// rounds end when one passes, when no edit is left to make, when a
// failure blames none of the round's edits, or after Rounds of them. The
// engine's postprocessing drops a tidy edit by making less of it the next
// round; drift acceptance rejects an accepted change, with an outcome.
type Verifier[E comparable, W any] struct {
	// Edit makes the edits in play and returns those it made. Making none
	// ends the rounds.
	Edit func(ctx context.Context) ([]E, error)
	// Verify returns nil if what the round's edits make passes, else what
	// fails.
	Verify func(ctx context.Context) (*Verdict[E, W], error)
	// Drop takes the edits a failure blamed out of play, each with why.
	Drop func(blamed map[E]W)
	// BlameAll has a failure that blames none of the round's edits blame
	// each of them, with its Why; without it, such a failure ends the
	// rounds, as no next round could make progress.
	BlameAll bool
	// Rounds bounds how many rounds Verify.
	Rounds int
}

// A Verdict is a failed verification: the edits it is Blamed on, each
// with why, and Why it fails as a whole.
type Verdict[E comparable, W any] struct {
	Blamed map[E]W
	Why    W
}

// Run runs the rounds and reports whether the last one's edits passed,
// which it leaves made. Otherwise the edits of the last round are as Edit
// left them.
func (v Verifier[E, W]) Run(ctx context.Context) (bool, error) {
	for range v.Rounds {
		made, err := v.Edit(ctx)
		if err != nil || len(made) == 0 {
			return false, err
		}
		failure, err := v.Verify(ctx)
		if err != nil || failure == nil {
			return err == nil, err
		}
		// What blames an edit the round didn't make tells nothing of
		// those it made.
		blamed := map[E]W{}
		for _, e := range made {
			if why, ok := failure.Blamed[e]; ok {
				blamed[e] = why
			}
		}
		if len(blamed) == 0 {
			if !v.BlameAll {
				return false, nil
			}
			for _, e := range made {
				blamed[e] = failure.Why
			}
		}
		v.Drop(blamed)
	}
	return false, nil
}

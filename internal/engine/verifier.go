// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "context"

// A verifier settles a set of edits by rounds: each makes the edits still
// in play on the files as they were, verifies what they make, such as by
// a plan or the checks of the gate, and, if that fails, drops the edits
// the failure is blamed on, for the next round to make the others. The
// rounds end when one passes, when no edit is left to make, when a
// failure blames none of the round's edits, or after rounds of them.
// Postprocessing drops a tidy edit by making less of it the next round
// (see verifyEach); drift acceptance rejects an accepted change, with an
// outcome (see acceptDrift).
type verifier[E comparable, W any] struct {
	// edit makes the edits in play and returns those it made. Making none
	// ends the rounds.
	edit func(ctx context.Context) ([]E, error)
	// verify returns nil if what the round's edits make passes, else what
	// fails.
	verify func(ctx context.Context) (*verdict[E, W], error)
	// drop takes the edits a failure blamed out of play, each with why.
	drop func(blamed map[E]W)
	// blameAll has a failure that blames none of the round's edits blame
	// each of them, with its why; without it, such a failure ends the
	// rounds, as no next round could make progress.
	blameAll bool
	// rounds bounds how many rounds verify.
	rounds int
}

// A verdict is a failed verification: the edits it is blamed on, each
// with why, and why it fails as a whole.
type verdict[E comparable, W any] struct {
	blamed map[E]W
	why    W
}

// run runs the rounds and reports whether the last one's edits passed,
// which it leaves made. Otherwise the edits of the last round are as edit
// left them.
func (v verifier[E, W]) run(ctx context.Context) (bool, error) {
	for range v.rounds {
		made, err := v.edit(ctx)
		if err != nil || len(made) == 0 {
			return false, err
		}
		failure, err := v.verify(ctx)
		if err != nil || failure == nil {
			return err == nil, err
		}
		// What blames an edit the round didn't make tells nothing of
		// those it made.
		blamed := map[E]W{}
		for _, e := range made {
			if why, ok := failure.blamed[e]; ok {
				blamed[e] = why
			}
		}
		if len(blamed) == 0 {
			if !v.blameAll {
				return false, nil
			}
			for _, e := range made {
				blamed[e] = failure.why
			}
		}
		v.drop(blamed)
	}
	return false, nil
}

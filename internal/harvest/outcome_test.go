// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// codedError carries the codes a test table reads with codesOf.
type codedError struct {
	codes []string
}

func (e *codedError) Error() string {
	return "codes " + strings.Join(e.codes, ",")
}

// testCodes is a table of every outcome, with exact codes ahead of the
// patterns, and patterns in order.
var testCodes = &Codes{
	Of: func(err error) []string {
		var coded *codedError
		if errors.As(err, &coded) {
			return coded.codes
		}
		return nil
	},
	Exact: map[string]Outcome{
		"Denied":        OutcomeFail,
		"NoAPI":         OutcomeNothingToList,
		"NoRegion":      OutcomeSkipRegion,
		"501":           OutcomeNothingToList,
		"TableNotFound": OutcomeFail,
	},
	Patterns: []CodePattern{
		{"*NotFound", OutcomeGone},
		{"5??", OutcomeFail},
		{"[1-4]??", OutcomeNothingToList},
	},
}

// The classifier decides each outcome once: a partial error before any
// code, an exact code before a pattern, the patterns in order, the first
// code the table names among several, and anything it doesn't name, or
// any error without a table, as a failure.
func TestClassify(t *testing.T) {
	t.Parallel()
	coded := func(codes ...string) error { return &codedError{codes: codes} }
	for _, tc := range []struct {
		name  string
		codes *Codes
		err   error
		want  Outcome
	}{
		{"exact failure", testCodes, coded("Denied"), OutcomeFail},
		{"gone by pattern", testCodes, coded("BucketNotFound"), OutcomeGone},
		{"wrapped", testCodes, fmt.Errorf("listing: %w", coded("BucketNotFound")), OutcomeGone},
		{"nothing to list", testCodes, coded("NoAPI"), OutcomeNothingToList},
		{"skip region", testCodes, coded("NoRegion"), OutcomeSkipRegion},
		{"partial before its code", testCodes, Partial(coded("NoAPI")), OutcomePartial},
		{"partial without a table", nil, fmt.Errorf("x: %w", Partial(errors.New("refused"))), OutcomePartial},
		{"exact before pattern", testCodes, coded("TableNotFound"), OutcomeFail},
		{"exact status before its range", testCodes, coded("501"), OutcomeNothingToList},
		{"patterns in order", testCodes, coded("503"), OutcomeFail},
		{"later pattern", testCodes, coded("409"), OutcomeNothingToList},
		{"first named code decides", testCodes, coded("Unnamed", "NoRegion", "Denied"), OutcomeSkipRegion},
		{"code the table doesn't name", testCodes, coded("Throttled"), OutcomeFail},
		{"no code", testCodes, errors.New("connection reset"), OutcomeFail},
		{"no table", nil, coded("BucketNotFound"), OutcomeFail},
		{"table without Of", &Codes{}, coded("BucketNotFound"), OutcomeFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.codes.Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Only a gone resource and nothing to list leave nothing out.
func TestLeavesNothingOut(t *testing.T) {
	t.Parallel()
	for outcome, want := range map[Outcome]bool{
		OutcomeFail:          false,
		OutcomePartial:       false,
		OutcomeGone:          true,
		OutcomeNothingToList: true,
		OutcomeSkipRegion:    false,
	} {
		if got := outcome.LeavesNothingOut(); got != want {
			t.Errorf("%v: LeavesNothingOut = %v, want %v", outcome, got, want)
		}
	}
}

// A part that failed makes the listing partial, naming the part; one the
// table says leaves nothing out is no failure; without a table every
// error is one; without failures, there is no error.
func TestParts(t *testing.T) {
	t.Parallel()
	var none Parts
	if none.Add(testCodes, "nothing", nil) || none.Err() != nil || none.ErrOutOf(0) != nil {
		t.Errorf("without failures: %v", none.Err())
	}

	var parts Parts
	for what, err := range map[string]error{
		"listeners of lb-1": &codedError{codes: []string{"LoadBalancerNotFound"}},
		"grants of key k":   &codedError{codes: []string{"NoAPI"}},
	} {
		if parts.Add(testCodes, what, err) {
			t.Errorf("%s: recorded %v, which leaves nothing out", what, err)
		}
	}
	if !parts.Add(testCodes, "rules of listener l-1", &codedError{codes: []string{"Denied"}}) {
		t.Error("a refused part should be recorded")
	}
	if !parts.Add(nil, "", &codedError{codes: []string{"LoadBalancerNotFound"}}) {
		t.Error("without a table, every error should be recorded")
	}
	err := parts.Err()
	if !IsPartial(err) || err.Error() != "rules of listener l-1: codes Denied\ncodes LoadBalancerNotFound" {
		t.Fatalf("want a partial error for the rules and the unnamed part, got %v", err)
	}

	var page Parts
	page.Add(nil, "policy of b", errors.New("denied"))
	parts.Merge(&page)
	if got := parts.Err().Error(); !strings.HasSuffix(got, "\npolicy of b: denied") {
		t.Errorf("Merge should add the other's failures after: %q", got)
	}
}

// The listing reads as failed when every part failed, or when it failed
// itself, and as partial otherwise.
func TestPartsErrOutOfAndFail(t *testing.T) {
	t.Parallel()
	var parts Parts
	parts.Add(nil, "channels", errors.New("denied"))
	if err := parts.ErrOutOf(2); !IsPartial(err) {
		t.Errorf("one of two failed: want partial, got %v", err)
	}
	parts.Add(nil, "inputs", errors.New("denied"))
	if err := parts.ErrOutOf(2); err == nil || IsPartial(err) {
		t.Errorf("both failed: want a failure, got %v", err)
	}
	listing := errors.New("list failed")
	err := parts.Fail(listing)
	if !errors.Is(err, listing) || IsPartial(err) || !strings.HasPrefix(err.Error(), "list failed\nchannels: denied") {
		t.Errorf("Fail: got %v", err)
	}
	var none Parts
	if err := none.Fail(listing); err.Error() != "list failed" || IsPartial(err) {
		t.Errorf("Fail without parts: got %v", err)
	}
}

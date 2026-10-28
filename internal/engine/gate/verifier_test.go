// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestVerifier(t *testing.T) {
	broken := errors.New("plan broke")
	tests := []struct {
		name string
		// verify judges the edits a round made: nil passes them.
		verify   func(made []string) *Verdict[string, string]
		blameAll bool
		rounds   int
		passed   bool
		err      error
		verifies int
		left     []string
		dropped  map[string]string
	}{{
		name:     "everything passes",
		verify:   func([]string) *Verdict[string, string] { return nil },
		rounds:   3,
		passed:   true,
		verifies: 1,
		left:     []string{"a", "b", "c"},
		dropped:  map[string]string{},
	}, {
		name: "one edit blamed and the others retried",
		verify: func(made []string) *Verdict[string, string] {
			if slices.Contains(made, "b") {
				return &Verdict[string, string]{Blamed: map[string]string{"b": "b breaks the plan"}}
			}
			return nil
		},
		rounds:   3,
		passed:   true,
		verifies: 2,
		left:     []string{"a", "c"},
		dropped:  map[string]string{"b": "b breaks the plan"},
	}, {
		name: "nothing attributable rejects them all",
		verify: func([]string) *Verdict[string, string] {
			return &Verdict[string, string]{Why: "the plan fails"}
		},
		blameAll: true,
		rounds:   3,
		verifies: 1,
		dropped:  map[string]string{"a": "the plan fails", "b": "the plan fails", "c": "the plan fails"},
	}, {
		name: "nothing attributable ends the rounds without blameAll",
		verify: func([]string) *Verdict[string, string] {
			// An edit the round didn't make tells nothing of those it made.
			return &Verdict[string, string]{Blamed: map[string]string{"z": "z breaks the plan"}}
		},
		rounds:   3,
		verifies: 1,
		left:     []string{"a", "b", "c"},
		dropped:  map[string]string{},
	}, {
		name: "the round bound is reached",
		verify: func(made []string) *Verdict[string, string] {
			return &Verdict[string, string]{Blamed: map[string]string{made[0]: made[0] + " breaks the plan"}}
		},
		rounds:   2,
		verifies: 2,
		left:     []string{"c"},
		dropped:  map[string]string{"a": "a breaks the plan", "b": "b breaks the plan"},
	}, {
		name: "an error ends the rounds",
		verify: func([]string) *Verdict[string, string] {
			return nil
		},
		rounds:   3,
		err:      broken,
		verifies: 1,
		left:     []string{"a", "b", "c"},
		dropped:  map[string]string{},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := []string{"a", "b", "c"}
			dropped := map[string]string{}
			var made []string
			verifies := 0
			passed, err := Verifier[string, string]{
				Edit: func(context.Context) ([]string, error) {
					made = slices.Clone(left)
					return made, nil
				},
				Verify: func(context.Context) (*Verdict[string, string], error) {
					verifies++
					return tt.verify(made), tt.err
				},
				Drop: func(blamed map[string]string) {
					for e, why := range blamed {
						dropped[e] = why
					}
					left = slices.DeleteFunc(left, func(e string) bool {
						_, ok := blamed[e]
						return ok
					})
				},
				BlameAll: tt.blameAll,
				Rounds:   tt.rounds,
			}.Run(context.Background())
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if passed != tt.passed {
				t.Errorf("passed = %v, want %v", passed, tt.passed)
			}
			if verifies != tt.verifies {
				t.Errorf("verified %d times, want %d", verifies, tt.verifies)
			}
			if !slices.Equal(left, tt.left) {
				t.Errorf("left %v, want %v", left, tt.left)
			}
			if !reflect.DeepEqual(dropped, tt.dropped) {
				t.Errorf("dropped %v, want %v", dropped, tt.dropped)
			}
		})
	}
}

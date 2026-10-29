// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providertest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Contract is what every lister of a provider must do, whatever it lists:
// list nothing, and fail with nothing, when the cloud has nothing; list
// only the types it declares; fail when the cloud refuses it, as the
// provider's codes classify a failure or a partial listing, never a
// listing with nothing left out; and stop when its context is done.
type Contract struct {
	// Declared are the Terraform types the lister declares it lists.
	Declared []string
	// List lists, in ctx, with a new lister of the provider whose
	// requests the cloud answers.
	List func(ctx context.Context, t *testing.T, cloud *Cloud) ([]harvest.Resource, error)
	// Empty answers every request as a cloud with nothing in it does.
	Empty Handler
	// Denied refuses every request, as a cloud refuses credentials that
	// may not read.
	Denied Handler
	// Listed answers with a resource of the lister's, or is nil where the
	// provider's own tests list each lister's resources (see
	// CheckDeclared).
	Listed Handler
	// Codes are the provider's codes (see harvest.Codes).
	Codes *harvest.Codes
}

// Run runs the contract's cases, each a subtest, which fails rather than
// panics when the lister panics.
func (c Contract) Run(t *testing.T) {
	t.Helper()
	t.Run("empty", func(t *testing.T) {
		resources, err := c.list(t.Context(), t, c.Empty)
		if err != nil || len(resources) != 0 {
			t.Errorf("an empty cloud: listed %d resources, error %v; want none and no error", len(resources), err)
		}
	})
	if c.Listed != nil {
		t.Run("listed", func(t *testing.T) {
			resources, err := c.list(t.Context(), t, c.Listed)
			if err != nil {
				t.Errorf("a listed resource: %v", err)
			}
			if len(resources) == 0 {
				t.Error("a listed resource: listed none")
			}
			CheckDeclared(t, resources, c.Declared)
		})
	}
	t.Run("denied", func(t *testing.T) {
		resources, err := c.list(t.Context(), t, c.Denied)
		if err == nil {
			t.Fatalf("a refused listing: listed %d resources and no error, want an error", len(resources))
		}
		switch outcome := c.Codes.Classify(err); outcome {
		case harvest.OutcomeFail, harvest.OutcomePartial:
		default:
			t.Errorf("a refused listing: %v is classified %s, want fail or partial", err, outcome)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		resources, err := c.list(ctx, t, c.Empty)
		if !errors.Is(err, context.Canceled) || len(resources) != 0 {
			t.Errorf("a canceled listing: listed %d resources, error %v; want none and context.Canceled", len(resources), err)
		}
	})
}

// list lists with handle answering, in ctx, and turns a panic into a
// failure of t.
func (c Contract) list(ctx context.Context, t *testing.T, handle Handler) (resources []harvest.Resource, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("the lister panicked: %v", r)
			resources, err = nil, nil
		}
	}()
	return c.List(ctx, t, New(handle))
}

// CheckDeclared fails t for each resource whose type isn't one of
// declared, the types its lister declares it lists.
func CheckDeclared(t testing.TB, resources []harvest.Resource, declared []string) {
	t.Helper()
	for _, r := range resources {
		if !slices.Contains(declared, r.Address.Type) {
			t.Errorf("listed %s %s, a type the lister doesn't declare (%v)", r.Address.Type, r.Observed.ID, declared)
		}
	}
}

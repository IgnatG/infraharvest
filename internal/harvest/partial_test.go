// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"errors"
	"fmt"
	"testing"
)

// A partial error is told apart through the errors that wrap it, but not
// through one that joins it to others, and keeps what it wraps.
func TestPartial(t *testing.T) {
	refused := errors.New("403 Forbidden")
	if Partial(nil) != nil {
		t.Error("Partial(nil) should be nil")
	}
	partial := Partial(refused)
	if !errors.Is(partial, refused) || partial.Error() != refused.Error() {
		t.Errorf("Partial should wrap the error as it is, got %v", partial)
	}
	for err, want := range map[error]bool{
		partial:                                true,
		fmt.Errorf("service x: %w", partial):   true,
		Partial(errors.Join(refused, refused)): true,
		refused:                                false,
		errors.Join(partial, errors.New("no access")): false,
		nil: false,
	} {
		if got := IsPartial(err); got != want {
			t.Errorf("IsPartial(%v): got %v, want %v", err, got, want)
		}
	}
}

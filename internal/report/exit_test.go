// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{errors.New("no"), ExitCouldNotRun},
		{&ExitError{Code: ExitPartial, Err: errors.New("partial")}, ExitPartial},
		{fmt.Errorf("wrapped: %w", &ExitError{Code: ExitIncomplete, Err: errors.New("x")}), ExitIncomplete},
	} {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

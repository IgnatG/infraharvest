// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// A root of more than LargeRootResources resources gets a warning that
// names --engine-parallelism and --layout; one of that many or fewer none.
func TestWarnLargeRoot(t *testing.T) {
	var logged bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(writer) })

	warnLargeRoot("out/aws/main", LargeRootResources)
	if logged.Len() != 0 {
		t.Errorf("a root of %d resources was warned about: %s", LargeRootResources, logged.String())
	}
	warnLargeRoot("out/aws/main", LargeRootResources+1)
	for _, want := range []string{"out/aws/main", "2001 resources", "--engine-parallelism", "--layout"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("warning %q lacks %q", logged.String(), want)
		}
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/secretdetect/parity"
)

// The detectors this package offers give what they gave before they moved
// to internal/secretdetect, byte for byte: the secrets check, the export
// and the drift summary all depend on them.
func TestDetectorsAreUnchanged(t *testing.T) {
	t.Parallel()
	parity.Check(t, parity.Detectors{
		IsName: IsName, CredentialFormat: CredentialFormat, Reason: Reason,
		InFile: func(rel, content string) []string { return InFile(NewMatcher(nil), rel, content) },
	})
}

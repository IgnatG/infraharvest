// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/secretdetect/parity"
)

// redacting adapts a redactor to what the parity corpus takes.
type redacting struct{ r *redactor }

func (x redacting) Tags(tags map[string]string, id, name string) map[string]string {
	return x.r.tags(tags, id, name)
}
func (x redacting) Dropped() []string    { return x.r.droppedKeys() }
func (x redacting) Hash(s string) string { return x.r.hash(s) }

// The export's tag filter and hash give what they gave before they moved
// to pkg/secretscan, byte for byte: the hashes in an export are how a
// control plane tells resources apart across exports.
func TestRedactorIsUnchanged(t *testing.T) {
	t.Parallel()
	parity.Check(t, parity.Detectors{NewTagger: func(drop []string, all bool, key []byte) parity.Tagger {
		return redacting{newRedactor(drop, all, key)}
	}})
}

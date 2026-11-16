// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

// What only the tests use.

// Lookup returns where the state of a resource of typ with any of ids is.
func (r Resources) Lookup(typ string, ids ...string) (string, bool) {
	res, ok := r.Find(typ, ids...)
	return res.Where, ok
}

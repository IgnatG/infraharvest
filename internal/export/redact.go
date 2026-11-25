// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"github.com/IgnatG/infraharvest/pkg/secretscan"
)

// KeyEnv is the environment variable that holds the key --hash-ids hashes
// with, if --hash-key-file doesn't name a file.
const KeyEnv = secretscan.KeyEnv

// ReadKey returns the key --hash-ids hashes with: the content of file if
// it is set, else the environment variable KeyEnv (see getenv), without
// surrounding white space. The key stays with the customer: export never
// writes it.
func ReadKey(file string, getenv func(string) string) ([]byte, error) {
	return secretscan.ReadKey(file, getenv)
}

// redactor decides which tags go into the export, and hashes what the
// customer asked to hash. The tag filter and the hash are those of
// secretscan, which tools that read the export share.
type redactor struct {
	// key hashes IDs and names, if not nil.
	key    []byte
	filter *secretscan.TagFilter
}

func newRedactor(drop []string, all bool, key []byte) *redactor {
	return &redactor{key: key, filter: secretscan.NewTagFilter(drop, all, key)}
}

// tags returns the tags of a resource whose ID and name are id and name
// that stay in the export (see secretscan.TagFilter.Tags).
func (r *redactor) tags(tags map[string]string, id, name string) map[string]string {
	return r.filter.Tags(tags, id, name)
}

// droppedKeys returns the keys it dropped, sorted. They are non-nil, as
// the schema wants a list.
func (r *redactor) droppedKeys() []string {
	return r.filter.DroppedKeys()
}

// hash returns the HMAC-SHA256 of s with the key, in hexadecimal, or s
// itself without a key (see secretscan.Hash).
func (r *redactor) hash(s string) string {
	return secretscan.Hash(r.key, s)
}

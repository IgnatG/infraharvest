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

// tags returns the tags of a resource that stay in the export: one whose ID,
// cloud ID (such as an ARN, or "") and name are id, cloudID and name. A tag
// that holds either ID is treated alike (see secretscan.TagFilter.TagsFor).
func (r *redactor) tags(tags map[string]string, id, cloudID, name string) map[string]string {
	return r.filter.TagsFor(tags, name, id, cloudID)
}

// droppedKeys returns the keys it dropped, sorted. They are non-nil, as
// the schema wants a list.
func (r *redactor) droppedKeys() []string {
	return r.filter.DroppedKeys()
}

// hash returns the HMAC-SHA256 of s with the key, in hexadecimal, or s
// itself without a key, with an Azure resource ID lower-cased first (see
// secretscan.HashID).
func (r *redactor) hash(s string) string {
	return secretscan.HashID(r.key, s)
}

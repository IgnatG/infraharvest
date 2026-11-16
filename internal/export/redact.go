// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// KeyEnv is the environment variable that holds the key --hash-ids hashes
// with, if --hash-key-file doesn't name a file.
const KeyEnv = "INFRAHARVEST_EXPORT_KEY"

// minKeyLength is the shortest key --hash-ids accepts, in bytes: a short
// one would let whoever holds the export try every key.
const minKeyLength = 16

// ReadKey returns the key --hash-ids hashes with: the content of file if
// it is set, else the environment variable KeyEnv (see getenv), without
// surrounding white space. The key stays with the customer: export never
// writes it.
func ReadKey(file string, getenv func(string) string) ([]byte, error) {
	var key []byte
	switch {
	case file != "":
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("--hash-key-file: %w", err)
		}
		key = bytes.TrimSpace(content)
	case getenv(KeyEnv) != "":
		key = bytes.TrimSpace([]byte(getenv(KeyEnv)))
	default:
		return nil, fmt.Errorf("--hash-ids needs a key you hold: set %s, or name a file with --hash-key-file", KeyEnv)
	}
	if len(key) < minKeyLength {
		return nil, errors.New("the hash key must have at least 16 bytes: use a random one, such as the output of openssl rand -hex 32")
	}
	return key, nil
}

// redactor decides which tags go into the export, and hashes what the
// customer asked to hash.
type redactor struct {
	// drop are the tag keys --drop-tag names; all is --drop-tags.
	drop map[string]bool
	all  bool
	// key hashes IDs and names, if not nil.
	key []byte
	// dropped are the tag keys it dropped, which the export lists (see
	// redaction).
	dropped map[string]bool
}

func newRedactor(drop []string, all bool, key []byte) *redactor {
	r := &redactor{drop: map[string]bool{}, all: all, key: key, dropped: map[string]bool{}}
	for _, k := range drop {
		r.drop[k] = true
	}
	return r
}

// tags returns the tags of a resource whose ID and name are id and name
// that stay in the export: none with --drop-tags, else all but those
// --drop-tag names and those the secret detectors flag, by key or value.
// With --hash-ids, a tag doesn't give away what the ID and name are hashed
// to hide (see hidesName).
func (r *redactor) tags(tags map[string]string, id, name string) map[string]string {
	var kept map[string]string
	for k, v := range tags {
		// A key that is itself a credential is dropped without being
		// listed, however it came to be dropped.
		listable := secret.CredentialFormat(k) == ""
		switch {
		case r.all, r.drop[k], looksSecret(k, v):
			if listable {
				r.dropped[k] = true
			}
		case r.key != nil && (namedIn(k, id) || namedIn(k, name)):
			// The key itself names the resource, such as
			// kubernetes.io/cluster/<name>: dropped, and not listed, as
			// the list would show it.
		default:
			if kept == nil {
				kept = map[string]string{}
			}
			if r.key != nil && hidesName(k, v, id, name) {
				v = r.hash(v)
			}
			kept[k] = v
		}
	}
	return kept
}

// minNamed is the shortest ID or name that a tag holding it as part of its
// key or value gives away: a shorter one, such as a name of one letter,
// would be found in tags that don't name the resource.
const minNamed = 4

// hidesName reports whether the tag k=v gives away the resource's ID or
// name, which --hash-ids hashes: a Name tag, a value that is the ID or the
// name, or a value that holds one of them, such as an ARN.
func hidesName(k, v, id, name string) bool {
	switch {
	case strings.EqualFold(k, "name"):
		return true
	case v != "" && (v == id || v == name):
		return true
	}
	return namedIn(v, id) || namedIn(v, name)
}

// namedIn reports whether s holds what, if what is long enough to give it
// away (see minNamed).
func namedIn(s, what string) bool {
	return len(what) >= minNamed && strings.Contains(s, what)
}

// looksSecret reports whether a tag's key or value looks like a secret:
// the key names one, or the key or the value has a credential's format, or
// the detectors flag the value for the key.
func looksSecret(key, value string) bool {
	return secret.IsName(key) || secret.CredentialFormat(key) != "" || secret.Reason(key, value, true) != ""
}

// droppedKeys returns the keys it dropped, sorted. They are non-nil, as
// the schema wants a list.
func (r *redactor) droppedKeys() []string {
	keys := make([]string, 0, len(r.dropped))
	for k := range r.dropped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// hash returns the HMAC-SHA256 of s with the key, in hexadecimal, or s
// itself without a key. The same key gives the same hash for the same
// value, so a control plane can tell resources apart across exports; only
// whoever has the key can check a guess.
func (r *redactor) hash(s string) string {
	if r.key == nil || s == "" {
		return s
	}
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secretscan

import (
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/secretdetect"
)

// TagFilter decides which of a resource's tags go into a report, and
// hashes what the user asked to hash. It is what infraharvest export
// applies to the tags of the inventory, with --drop-tag, --drop-tags and
// --hash-ids. A TagFilter is not safe for concurrent use.
type TagFilter struct {
	// drop are the tag keys named to drop; all drops every tag.
	drop map[string]bool
	all  bool
	// key hashes IDs and names, if not nil.
	key []byte
	// dropped are the tag keys it dropped, which a report lists.
	dropped map[string]bool
}

// NewTagFilter returns a filter that drops the tags with the keys dropKeys,
// or every tag if dropAll is set, and any tag that looks like it holds a
// secret (see LooksSecret). If hashKey is not nil, it also hashes (see
// Hash) the tags that give away the ID or the name of their resource. It
// panics for a hashKey shorter than MinKeyLength bytes, which would give the
// hashes away to a guess; NewTagFilterChecked returns an error for one
// instead.
func NewTagFilter(dropKeys []string, dropAll bool, hashKey []byte) *TagFilter {
	mustKey(hashKey)
	f := &TagFilter{drop: map[string]bool{}, all: dropAll, key: hashKey, dropped: map[string]bool{}}
	for _, k := range dropKeys {
		f.drop[k] = true
	}
	return f
}

// NewTagFilterChecked is NewTagFilter that returns an error, and no filter,
// for a hashKey shorter than MinKeyLength bytes (see CheckKey), instead of
// panicking.
func NewTagFilterChecked(dropKeys []string, dropAll bool, hashKey []byte) (*TagFilter, error) {
	if err := CheckKey(hashKey); err != nil {
		return nil, err
	}
	return NewTagFilter(dropKeys, dropAll, hashKey), nil
}

// Tags returns the tags of a resource whose ID and name are id and name
// that stay in a report: none if the filter drops all, else all but those
// it was told to drop and those that look like they hold a secret, by key
// or value. With a hash key, a tag doesn't give away what the ID and name
// are hashed to hide: a tag whose key names the resource is dropped, and a
// Name tag, or one whose value is or holds the ID or the name in any case, such as
// an ARN, has its value hashed (see HashID). It returns nil if no tag stays.
func (f *TagFilter) Tags(tags map[string]string, id, name string) map[string]string {
	return f.TagsFor(tags, name, id)
}

// TagsFor is Tags for a resource known by more than one ID, such as an ID
// and an ARN: a tag that names any of ids is treated as one that names the
// ID.
func (f *TagFilter) TagsFor(tags map[string]string, name string, ids ...string) map[string]string {
	var kept map[string]string
	for k, v := range tags {
		// A key that is itself a credential, or that names the resource
		// whose ID and name are hashed (such as kubernetes.io/cluster/<name>),
		// is dropped without being listed, however it came to be dropped:
		// the list would show it.
		names := f.key != nil && (namedIn(k, name) || namedInAny(k, ids))
		listable := secretdetect.CredentialFormat(k) == "" && !names
		switch {
		case f.all, f.drop[k], LooksSecret(k, v):
			if listable {
				f.dropped[k] = true
			}
		case names:
			// The key itself names the resource, such as
			// kubernetes.io/cluster/<name>: dropped, and not listed, as
			// the list would show it.
		default:
			if kept == nil {
				kept = map[string]string{}
			}
			if f.key != nil && hidesName(k, v, ids, name) {
				v = HashID(f.key, v)
			}
			kept[k] = v
		}
	}
	return kept
}

// DroppedKeys returns the tag keys Tags dropped so far, sorted, except
// those that would give a credential or a hashed name away by being
// listed. It is never nil, so that a report can list it.
func (f *TagFilter) DroppedKeys() []string {
	keys := make([]string, 0, len(f.dropped))
	for k := range f.dropped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// minNamed is the shortest ID or name that a tag holding it as part of its
// key or value gives away: a shorter one, such as a name of one letter,
// would be found in tags that don't name the resource.
const minNamed = 4

// hidesName reports whether the tag k=v gives away the resource's ID or
// name, which --hash-ids hashes: a Name tag, a value that is the ID or the
// name, or a value that holds one of them, such as an ARN.
func hidesName(k, v string, ids []string, name string) bool {
	if strings.EqualFold(k, "name") || (v != "" && strings.EqualFold(v, name)) {
		return true
	}
	for _, id := range ids {
		if v != "" && strings.EqualFold(v, id) {
			return true
		}
	}
	return namedIn(v, name) || namedInAny(v, ids)
}

// namedInAny reports whether s holds any of whats (see namedIn).
func namedInAny(s string, whats []string) bool {
	for _, w := range whats {
		if namedIn(s, w) {
			return true
		}
	}
	return false
}

// namedIn reports whether s holds what, in any case, if what is long enough
// to give it away (see minNamed). Case is ignored because an Azure ID is the
// same ID in any case, and a tag may spell it differently from the resource.
func namedIn(s, what string) bool {
	return len(what) >= minNamed && strings.Contains(strings.ToLower(s), strings.ToLower(what))
}

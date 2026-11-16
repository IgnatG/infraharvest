// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// checkpointCache keeps, for a command, what each checkpoint file it has
// read records, by the file's path and the hash of its content, so that
// the index each scope opens (see Run.openOutput) reads a checkpoint again
// from disk but decodes it again only when it changed, as when a scope
// before it generated the root again, and every scope's index holds the
// same decoded checkpoint rather than one each. What it hands out is
// shared: it must not be changed, as the index's own answers aren't. Safe
// for concurrent use; the zero value is ready.
type checkpointCache struct {
	mu     sync.Mutex
	roots  map[string]decoded[checkpoint]
	merges map[string]decoded[mergeCheckpoint]
}

// decoded is a checkpoint file's content as decoded, with the hash of the
// content it was decoded from.
type decoded[T any] struct {
	sum [sha256.Size]byte
	c   *T
	err error
}

// root and merge return the checkpoint of the root, or merged root, in
// dir under out, as checkpointTree.read does.
func (k *checkpointCache) root(out, dir string) (*checkpoint, error) {
	return readCached(k, &k.roots, rootCheckpoints, out, dir)
}

func (k *checkpointCache) merge(out, dir string) (*mergeCheckpoint, error) {
	return readCached(k, &k.merges, mergeCheckpoints, out, dir)
}

func readCached[T any](k *checkpointCache, cache *map[string]decoded[T], t checkpointTree[T], out, dir string) (*T, error) {
	path := t.path(out, dir)
	// The file is hashed as it is read, without holding it: only one that
	// changed is read whole, to be decoded.
	sum, err := hashFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	d, ok := (*cache)[path]
	k.mu.Unlock()
	if !ok || d.sum != sum {
		content, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		// What is decoded is what was read, which may have changed since
		// it was hashed.
		d = decoded[T]{sum: sha256.Sum256(content), c: new(T)}
		if err := json.Unmarshal(content, d.c); err != nil {
			d.c, d.err = nil, fmt.Errorf("%s: %w", path, err)
		}
		k.mu.Lock()
		if *cache == nil {
			*cache = map[string]decoded[T]{}
		}
		(*cache)[path] = d
		k.mu.Unlock()
	}
	return d.c, d.err
}

// hashFile returns the SHA-256 of the file at path.
func hashFile(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secretscan

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// KeyEnv is the environment variable that holds the key --hash-ids hashes
// with, if --hash-key-file doesn't name a file.
const KeyEnv = "INFRAHARVEST_EXPORT_KEY"

// minKeyLength is the shortest key --hash-ids accepts, in bytes: a short
// one would let whoever holds the export try every key.
const minKeyLength = 16

// ReadKey returns the key --hash-ids hashes with: the content of file if
// it is set, else the environment variable KeyEnv (see getenv, such as
// os.Getenv), without surrounding white space. The key stays with the
// customer: nothing here writes it. It fails for a key of fewer than 16
// bytes.
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

// Hash returns the HMAC-SHA256 of s with key, in hexadecimal, or s itself
// when key is nil or s is empty. The same key gives the same hash for the
// same value, so a control plane can tell resources apart across exports;
// only whoever has the key can check a guess. It is the hash of
// infraharvest export --hash-ids.
func Hash(key []byte, s string) string {
	if key == nil || s == "" {
		return s
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secretscan_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/secretdetect/parity"
	"github.com/IgnatG/infraharvest/pkg/secretscan"
)

// tagger is a TagFilter and the hash of its key, as the parity corpus
// takes a tag filter.
type tagger struct {
	*secretscan.TagFilter
	key []byte
}

func (t tagger) Dropped() []string    { return t.DroppedKeys() }
func (t tagger) Hash(s string) string { return secretscan.Hash(t.key, s) }

// The public detectors, the tag filter and the hash give what the
// internal ones of infraharvest export and infraharvest's secrets check
// gave before they were shared, byte for byte (see parity).
func TestParityWithTheInternalDetectors(t *testing.T) {
	t.Parallel()
	parity.Check(t, parity.Detectors{
		IsName:           secretscan.IsSecretName,
		CredentialFormat: secretscan.CredentialFormat,
		LooksSecret:      secretscan.LooksSecret,
		NewTagger: func(drop []string, all bool, key []byte) parity.Tagger {
			return tagger{secretscan.NewTagFilter(drop, all, key), key}
		},
	})
}

// --hash-ids hashes with HMAC-SHA256 of the key, in hexadecimal, as
// infraharvest export always did.
func TestHashIsHMACSHA256(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef-key")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("vpc-0abc1234"))
	if got, want := secretscan.Hash(key, "vpc-0abc1234"), hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := secretscan.Hash(nil, "vpc-0abc1234"); got != "vpc-0abc1234" {
		t.Errorf("without a key the value stays: %s", got)
	}
	if got := secretscan.Hash(key, ""); got != "" {
		t.Errorf("an empty value stays empty: %s", got)
	}
}

func TestReadKey(t *testing.T) {
	t.Parallel()
	none := func(string) string { return "" }
	fromEnv := func(k string) string {
		if k == secretscan.KeyEnv {
			return " an-environment-key-0123\n"
		}
		return ""
	}
	if _, err := secretscan.ReadKey("", none); err == nil || !strings.Contains(err.Error(), secretscan.KeyEnv) {
		t.Errorf("without a key: %v", err)
	}
	if key, err := secretscan.ReadKey("", fromEnv); err != nil || string(key) != "an-environment-key-0123" {
		t.Errorf("from the environment: %q, %v", key, err)
	}
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("a-key-with-enough-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := secretscan.ReadKey(file, fromEnv); err != nil || string(key) != "a-key-with-enough-bytes" {
		t.Errorf("the file wins over the environment: %q, %v", key, err)
	}
	if _, err := secretscan.ReadKey(filepath.Join(t.TempDir(), "missing"), none); err == nil {
		t.Error("want an error for a missing file")
	}
	if _, err := secretscan.ReadKey("", func(string) string { return "short" }); err == nil || !strings.Contains(err.Error(), "at least 16 bytes") {
		t.Errorf("a short key: %v", err)
	}
}

func TestDetectors(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]bool{"DB_PASSWORD": true, "clientSecret": true, "SECRET_ARN": false, "ACCESS_KEY_ID": false, "Name": false} {
		if got := secretscan.IsSecretName(key); got != want {
			t.Errorf("IsSecretName(%q) = %v, want %v", key, got, want)
		}
	}
	if got := secretscan.CredentialFormat("-----BEGIN " + "PRIVATE KEY-----"); got != "private key" {
		t.Errorf("CredentialFormat: %q", got)
	}
	if secretscan.LooksSecret("Environment", "prod") || !secretscan.LooksSecret("db_password", "hunter2x") {
		t.Error("LooksSecret does not tell a setting from a password")
	}
}

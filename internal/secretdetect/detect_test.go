// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secretdetect

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/secretdetect/parity"
)

func TestKeyWords(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"DB_PASSWORD":   "DB PASSWORD",
		"dbPassword":    "DB PASSWORD",
		"APIKey":        "API KEY",
		"client-secret": "CLIENT SECRET",
		"Oauth2Token":   "OAUTH2 TOKEN",
	} {
		if got := strings.Join(keyWords(key), " "); got != want {
			t.Errorf("keyWords(%q): got %q, want %q", key, got, want)
		}
	}
}

// The detectors give what they gave before they moved here, from
// internal/engine/secret, byte for byte.
func TestParity(t *testing.T) {
	t.Parallel()
	parity.Check(t, parity.Detectors{IsName: IsName, CredentialFormat: CredentialFormat, Reason: Reason})
}

// FileCredentials names the formats a file holds, in the order of the
// formats, and none for a file that holds none.
func TestFileCredentials(t *testing.T) {
	t.Parallel()
	if got := FileCredentials("nothing to see here\n"); len(got) != 0 {
		t.Errorf("a clean file: %v", got)
	}
	content := "-----BEGIN " + "PRIVATE KEY-----\ngl" + "pat-" + strings.Repeat("a", 20) + "\n"
	if got := strings.Join(FileCredentials(content), ","); got != "GitLab token,private key" {
		t.Errorf("got %q", got)
	}
}

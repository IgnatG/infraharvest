// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strings"
	"testing"
)

const googleKMSCluster = `resource "google_kms_key_ring" "app" {
  location = "us-central1"
  name     = "app"
  project  = "acme"
}
resource "google_kms_crypto_key" "signing" {
  deletion_policy = "DELETE"
  key_ring        = google_kms_key_ring.app.id
  labels          = { team = "app" }
  name            = "signing"
  purpose         = "ENCRYPT_DECRYPT"
  rotation_period = "2592000s"
  version_template {
    algorithm        = "GOOGLE_SYMMETRIC_ENCRYPTION"
    protection_level = "SOFTWARE"
  }
}
resource "google_kms_crypto_key" "data" {
  key_ring        = google_kms_key_ring.app.id
  labels          = { team = "app" }
  name            = "data"
  purpose         = "ENCRYPT_DECRYPT"
  rotation_period = "2592000s"
  version_template {
    algorithm        = "GOOGLE_SYMMETRIC_ENCRYPTION"
    protection_level = "SOFTWARE"
  }
}
`

func TestGoogleKMSMapsTheRingsKeys(t *testing.T) {
	call, err := GoogleKMS.Map(cluster(t, googleKMSCluster))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, GoogleKMS, call)
	for _, want := range []string{
		`keyring = "app"`,
		`project_id = "acme"`,
		`location = "us-central1"`,
		`keys = ["data", "signing"]`,
		`purpose = "ENCRYPT_DECRYPT"`,
		`key_rotation_period = "2592000s"`,
		`labels = { team = "app" }`,
		`key_algorithm = "GOOGLE_SYMMETRIC_ENCRYPTION"`,
		`key_protection_level = "SOFTWARE"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if call.Addresses["google_kms_crypto_key.data"] != "google_kms_crypto_key.key[0]" || call.Addresses["google_kms_crypto_key.signing"] != "google_kms_crypto_key.key[1]" {
		t.Errorf("addresses: %v", call.Addresses)
	}
	if got := call.Outputs["google_kms_crypto_key.signing"]["id"]; got != `keys["signing"]` {
		t.Errorf("signing's ID output: %s", got)
	}
}

// A key without rotation: the module's default rotates every 90 days.
func TestGoogleKMSKeepsKeysUnrotated(t *testing.T) {
	src := strings.ReplaceAll(googleKMSCluster, `  rotation_period = "2592000s"`+"\n", "")
	call, err := GoogleKMS.Map(cluster(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, GoogleKMS, call); !strings.Contains(got, "key_rotation_period = null") {
		t.Errorf("call:\n%s", got)
	}
}

func TestGoogleKMSDeclinesKeysThatDiffer(t *testing.T) {
	src := strings.Replace(googleKMSCluster, `rotation_period = "2592000s"`, `rotation_period = "86400s"`, 1)
	_, err := GoogleKMS.Map(cluster(t, src))
	if !IsDecline(err) || !strings.Contains(err.Error(), "have different settings") {
		t.Errorf("got %v", err)
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

// clearCredentialsEnv unsets the variables credentialOptions reads, for
// the test.
func clearCredentialsEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{accessTokenEnv, impersonateEnv}, credentialsEnvs...) {
		t.Setenv(name, "")
	}
}

// Without the Terraform provider's variables, the listers use the
// application default credentials, with the cloud-platform scope alone.
func TestCredentialOptionsDefault(t *testing.T) {
	clearCredentialsEnv(t)
	opts, err := credentialOptions(t.Context())
	if err != nil || len(opts) != 1 {
		t.Errorf("got %d options, %v; want the scope alone", len(opts), err)
	}
}

// An access token, and credentials given as JSON or as a file, are used
// as the Terraform provider uses them.
func TestCredentialOptionsFromTheEnvironment(t *testing.T) {
	key := `{"type": "service_account", "project_id": "acme-prod", "client_email": "deployer@acme-prod.iam.gserviceaccount.com"}`
	file := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(file, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]map[string]string{
		"access token":        {accessTokenEnv: "ya29.token"},
		"credentials JSON":    {"GOOGLE_CREDENTIALS": key},
		"credentials file":    {"GOOGLE_CREDENTIALS": file},
		"keyfile JSON":        {"GCLOUD_KEYFILE_JSON": file},
		"token before a file": {accessTokenEnv: "ya29.token", "GOOGLE_CREDENTIALS": "missing.json"},
	} {
		t.Run(name, func(t *testing.T) {
			clearCredentialsEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			opts, err := credentialOptions(t.Context())
			if err != nil || len(opts) != 2 {
				t.Errorf("got %d options, %v; want the scope and the credentials", len(opts), err)
			}
		})
	}
}

// Credentials that can't be used fail, naming the variable but not
// repeating its value.
func TestCredentialOptionsFail(t *testing.T) {
	for name, value := range map[string]string{
		"missing file": "no-such-key.json",
		"not JSON":     "{secret",
		"unknown type": `{"type": "something_else", "private_key": "secret"}`,
	} {
		t.Run(name, func(t *testing.T) {
			clearCredentialsEnv(t)
			t.Setenv("GOOGLE_CREDENTIALS", value)
			_, err := credentialOptions(t.Context())
			if err == nil || !strings.Contains(err.Error(), "GOOGLE_CREDENTIALS") || strings.Contains(err.Error(), "secret") {
				t.Errorf("error %v, want one naming GOOGLE_CREDENTIALS without its value", err)
			}
		})
	}
}

func TestCredentialsType(t *testing.T) {
	for credentials, want := range map[string]option.CredentialsType{
		`{"type": "service_account"}`:              option.ServiceAccount,
		`{"type": "authorized_user"}`:              option.AuthorizedUser,
		`{"type": "external_account"}`:             option.ExternalAccount,
		`{"type": "impersonated_service_account"}`: option.ImpersonatedServiceAccount,
	} {
		if got, err := credentialsType([]byte(credentials)); err != nil || got != want {
			t.Errorf("%s: got %q, %v", credentials, got, err)
		}
	}
}

// A token source for credentials that can't be used fails each request
// with why.
func TestFailingTokenSource(t *testing.T) {
	if _, err := (failingTokenSource{err: os.ErrNotExist}).Token(); err != os.ErrNotExist {
		t.Errorf("error %v", err)
	}
}

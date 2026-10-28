// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// writeFile writes content to a file named name in a test directory and
// returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The sign-in settings azurerm reads beyond the basic ones: a token or a
// token file for OIDC, Azure DevOps' variables, the managed identity API
// version, ARM_USE_CLI and a certificate passed in the environment.
func TestLoadAuthConfigReadsAzurermSettings(t *testing.T) {
	t.Parallel()
	certificate := []byte("pkcs12 bytes")
	cfg, err := loadAuthConfig(envOf(map[string]string{
		"ARM_OIDC_TOKEN":                         " id-token\n",
		"ARM_OIDC_TOKEN_FILE_PATH":               "/var/run/token",
		"SYSTEM_OIDCREQUESTURI":                  "https://dev.azure.example/oidc",
		"SYSTEM_ACCESSTOKEN":                     "pipeline-token",
		"ARM_OIDC_AZURE_SERVICE_CONNECTION_ID":   "connection",
		"ARM_MSI_API_VERSION":                    "2019-08-01",
		"ARM_USE_CLI":                            "false",
		"ARM_CLIENT_CERTIFICATE":                 base64.StdEncoding.EncodeToString(certificate),
		"ARM_CLIENT_CERTIFICATE_PASSWORD":        "pass",
		"ARM_ADO_PIPELINE_SERVICE_CONNECTION_ID": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := authConfig{
		oidcToken:              "id-token",
		oidcTokenFiles:         []tokenFile{{"ARM_OIDC_TOKEN_FILE_PATH", "/var/run/token"}},
		oidcRequestURL:         "https://dev.azure.example/oidc",
		oidcRequestToken:       "pipeline-token",
		adoServiceConnectionID: "connection",
		msiAPIVersion:          "2019-08-01",
		cliDisabled:            true,
		certificateData:        certificate,
		certificatePassword:    "pass",
		cloud:                  cloud.AzurePublic,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}

	// The Azure CLI is on unless ARM_USE_CLI turns it off.
	for value, disabled := range map[string]bool{"": false, "true": false, "0": true} {
		cfg, err := loadAuthConfig(envOf(map[string]string{"ARM_USE_CLI": value}))
		if err != nil || cfg.cliDisabled != disabled {
			t.Errorf("ARM_USE_CLI=%q: cliDisabled %v (%v), want %v", value, cfg.cliDisabled, err, disabled)
		}
	}

	_, err = loadAuthConfig(envOf(map[string]string{"ARM_CLIENT_CERTIFICATE": "not base64!"}))
	if err == nil || !strings.Contains(err.Error(), "ARM_CLIENT_CERTIFICATE is not base64") {
		t.Errorf("a certificate that isn't base64: got %v", err)
	}
}

// The client ID and secret can come from files, trimmed, as azurerm reads
// them; a file that disagrees with the variable, or can't be read, is an
// error that shows neither value.
func TestLoadAuthConfigReadsTheClientFiles(t *testing.T) {
	t.Parallel()
	idFile := writeFile(t, "client-id", "client\n")
	secretFile := writeFile(t, "client-secret", "  s3cret \n")
	cfg, err := loadAuthConfig(envOf(map[string]string{
		"ARM_CLIENT_ID":               "client",
		"ARM_CLIENT_ID_FILE_PATH":     idFile,
		"ARM_CLIENT_SECRET_FILE_PATH": secretFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.clientID != "client" || cfg.clientSecret != "s3cret" {
		t.Errorf("client ID %q, secret %q", cfg.clientID, cfg.clientSecret)
	}

	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"a different secret": {
			map[string]string{"ARM_CLIENT_SECRET": "other", "ARM_CLIENT_SECRET_FILE_PATH": secretFile},
			"ARM_CLIENT_SECRET and the file ARM_CLIENT_SECRET_FILE_PATH names differ",
		},
		"a missing file": {
			map[string]string{"ARM_CLIENT_ID_FILE_PATH": filepath.Join(t.TempDir(), "missing")},
			"reading ARM_CLIENT_ID_FILE_PATH",
		},
	} {
		_, err := loadAuthConfig(envOf(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, tc.want)
		}
		if err != nil && (strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "other")) {
			t.Errorf("%s: the error %q shows the secret", name, err)
		}
	}
}

// ARM_USE_AKS_WORKLOAD_IDENTITY turns on OIDC with the token file, client
// ID and tenant ID AKS sets, as azurerm does; an ARM_ ID that differs is
// an error.
func TestLoadAuthConfigAKSWorkloadIdentity(t *testing.T) {
	t.Parallel()
	aks := map[string]string{
		"ARM_USE_AKS_WORKLOAD_IDENTITY": "true",
		"AZURE_FEDERATED_TOKEN_FILE":    "/var/run/secrets/azure/tokens/azure-identity-token",
		"AZURE_CLIENT_ID":               "aks-client",
		"AZURE_TENANT_ID":               "aks-tenant",
	}
	cfg, err := loadAuthConfig(envOf(aks))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.useOIDC || cfg.clientID != "aks-client" || cfg.tenantID != "aks-tenant" {
		t.Errorf("useOIDC %v, client %q, tenant %q", cfg.useOIDC, cfg.clientID, cfg.tenantID)
	}
	if want := []tokenFile{{"AZURE_FEDERATED_TOKEN_FILE", aks["AZURE_FEDERATED_TOKEN_FILE"]}}; !reflect.DeepEqual(cfg.oidcTokenFiles, want) {
		t.Errorf("token files %v, want %v", cfg.oidcTokenFiles, want)
	}
	if got := cfg.method(); got != signInWithOIDCToken {
		t.Errorf("signs in with %s, want an OIDC token", got)
	}

	// The same IDs are no conflict.
	aks["ARM_CLIENT_ID"], aks["ARM_TENANT_ID"] = "aks-client", "aks-tenant"
	if _, err := loadAuthConfig(envOf(aks)); err != nil {
		t.Errorf("matching ARM_ IDs: %v", err)
	}
	aks["ARM_TENANT_ID"] = "other-tenant"
	if _, err := loadAuthConfig(envOf(aks)); err == nil || !strings.Contains(err.Error(), "ARM_TENANT_ID and AZURE_TENANT_ID") {
		t.Errorf("a different tenant: got %v", err)
	}

	// Without the flag, AKS's variables are not read.
	cfg, err = loadAuthConfig(envOf(map[string]string{"AZURE_FEDERATED_TOKEN_FILE": "/token", "AZURE_CLIENT_ID": "aks-client"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.useOIDC || cfg.clientID != "" || cfg.oidcTokenFiles != nil {
		t.Errorf("without ARM_USE_AKS_WORKLOAD_IDENTITY: %+v", cfg)
	}
}

// azurerm's order: a certificate (in the environment or a file), a secret,
// an OIDC token, Azure DevOps OIDC, GitHub-style OIDC, a managed identity,
// then the Azure CLI unless ARM_USE_CLI turns it off.
func TestSignInMethodOrderWithAzurermSettings(t *testing.T) {
	t.Parallel()
	oidc := authConfig{
		useOIDC:                true,
		oidcToken:              "id-token",
		oidcRequestURL:         "https://dev.azure.example/oidc",
		oidcRequestToken:       "request-token",
		adoServiceConnectionID: "connection",
		useMSI:                 true,
	}
	for name, tc := range map[string]struct {
		edit func(*authConfig)
		want signInMethod
	}{
		"a certificate from the environment": {func(c *authConfig) { c.certificateData = []byte("x") }, signInWithCertificate},
		"an OIDC token first":                {func(*authConfig) {}, signInWithOIDCToken},
		"or a token file": {func(c *authConfig) {
			c.oidcToken, c.oidcTokenFiles = "", []tokenFile{{"ARM_OIDC_TOKEN_FILE_PATH", "/token"}}
		}, signInWithOIDCToken},
		"then Azure DevOps": {func(c *authConfig) { c.oidcToken = "" }, signInWithADOPipeline},
		"then GitHub-style OIDC": {func(c *authConfig) {
			c.oidcToken, c.adoServiceConnectionID = "", ""
		}, signInWithOIDC},
		"a token needs the OIDC flag": {func(c *authConfig) { c.useOIDC = false }, signInWithManagedIdentity},
		"no CLI": {func(c *authConfig) {
			*c = authConfig{cliDisabled: true}
		}, signInWithNothing},
	} {
		cfg := oidc
		tc.edit(&cfg)
		if got := cfg.method(); got != tc.want {
			t.Errorf("%s: got %s, want %s", name, got, tc.want)
		}
	}
}

// Settings that ask for OIDC without a token, or turn the CLI off with
// nothing else set up, fail clearly instead of signing in some other way;
// OIDC without a token next to ARM_USE_MSI signs in as the identity, as
// azurerm does.
func TestNewCredentialFailsInsteadOfFallingBack(t *testing.T) {
	t.Parallel()
	application := authConfig{tenantID: "tenant", clientID: "client", cloud: cloud.AzurePublic}
	for name, tc := range map[string]struct {
		edit func(*authConfig)
		want error
	}{
		"OIDC without a token":         {func(c *authConfig) { c.useOIDC = true }, errNoIDToken},
		"OIDC without a request token": {func(c *authConfig) { c.useOIDC, c.oidcRequestURL = true, "https://oidc.example" }, errNoIDToken},
		"OIDC, no token and no CLI":    {func(c *authConfig) { c.useOIDC, c.cliDisabled = true, true }, errNoIDToken},
		"no CLI and nothing else":      {func(c *authConfig) { c.cliDisabled = true }, errNoSignIn},
	} {
		cfg := application
		tc.edit(&cfg)
		if _, err := newCredential(cfg); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}

	cfg := application
	cfg.useOIDC, cfg.useMSI, cfg.msiEndpoint = true, true, "https://msi.example/token"
	if _, err := newCredential(cfg); err != nil {
		t.Errorf("OIDC without a token, with a managed identity: %v", err)
	}
}

func TestNewCredentialAzurermTypes(t *testing.T) {
	t.Parallel()
	certificate, err := os.ReadFile(writeTestCertificate(t))
	if err != nil {
		t.Fatal(err)
	}
	application := authConfig{tenantID: "tenant", clientID: "client", cloud: cloud.AzurePublic}
	for name, tc := range map[string]struct {
		edit func(*authConfig)
		want string
	}{
		"certificate from ARM_CLIENT_CERTIFICATE": {func(c *authConfig) { c.certificateData = certificate }, "*azidentity.ClientCertificateCredential"},
		"OIDC token": {func(c *authConfig) {
			c.useOIDC, c.oidcToken = true, "id-token"
		}, "*azidentity.ClientAssertionCredential"},
		"Azure DevOps OIDC": {func(c *authConfig) {
			c.useOIDC, c.oidcRequestURL, c.oidcRequestToken, c.adoServiceConnectionID = true, "https://dev.azure.example/oidc", "token", "connection"
		}, "*azidentity.ClientAssertionCredential"},
	} {
		cfg := application
		tc.edit(&cfg)
		credential, err := newCredential(cfg)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := fmt.Sprintf("%T", credential); got != tc.want {
			t.Errorf("%s: got a %s, want a %s", name, got, tc.want)
		}
	}

	_, err = newCredential(authConfig{tenantID: "tenant", clientID: "client", certificateData: []byte("garbage"), cloud: cloud.AzurePublic})
	if err == nil || !strings.Contains(err.Error(), "parsing the certificate in ARM_CLIENT_CERTIFICATE:") {
		t.Errorf("a bad ARM_CLIENT_CERTIFICATE: got %v", err)
	}
}

func TestNewCredentialPassesTheMSIAPIVersion(t *testing.T) {
	t.Parallel()
	credential, err := newCredential(authConfig{useMSI: true, msiEndpoint: "https://msi.example/token", msiAPIVersion: "2019-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := credential.(*cachedCredential)
	if !ok {
		t.Fatalf("got a %T, want its tokens cached", credential)
	}
	if msi, ok := cached.credential.(*msiEndpointCredential); !ok || msi.apiVersion != "2019-08-01" {
		t.Errorf("got %#v", cached.credential)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api-version"); got != "2019-08-01" {
			t.Errorf("api-version %q", got)
		}
		_, _ = w.Write([]byte(`{"access_token":"msi-token","expires_in":3600}`))
	}))
	defer server.Close()
	msi := &msiEndpointCredential{endpoint: server.URL, apiVersion: "2019-08-01", client: server.Client()}
	if _, err := msi.GetToken(t.Context(), policy.TokenRequestOptions{Scopes: []string{"https://management.example/.default"}}); err != nil {
		t.Error(err)
	}
}

// The token files are read on each sign-in, so a token AKS rotates is
// picked up; tokens that disagree, or an empty one, are errors.
func TestIDToken(t *testing.T) {
	t.Parallel()
	file := writeFile(t, "token", "first\n")
	cfg := authConfig{oidcTokenFiles: []tokenFile{{"AZURE_FEDERATED_TOKEN_FILE", file}}}
	if token, err := cfg.idToken(); err != nil || token != "first" {
		t.Errorf("got %q, %v", token, err)
	}
	if err := os.WriteFile(file, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := cfg.idToken(); err != nil || token != "rotated" {
		t.Errorf("after the rotation: got %q, %v", token, err)
	}

	for name, tc := range map[string]struct {
		cfg  authConfig
		want string
	}{
		"a token and a file that differ": {
			authConfig{oidcToken: "other", oidcTokenFiles: []tokenFile{{"ARM_OIDC_TOKEN_FILE_PATH", file}}},
			"ARM_OIDC_TOKEN and ARM_OIDC_TOKEN_FILE_PATH hold different OIDC tokens",
		},
		"a missing file": {
			authConfig{oidcTokenFiles: []tokenFile{{"ARM_OIDC_TOKEN_FILE_PATH", filepath.Join(t.TempDir(), "missing")}}},
			"reading the OIDC token in ARM_OIDC_TOKEN_FILE_PATH",
		},
		"an empty file": {
			authConfig{oidcTokenFiles: []tokenFile{{"AZURE_FEDERATED_TOKEN_FILE", writeFile(t, "empty", " \n")}}},
			"the OIDC token in AZURE_FEDERATED_TOKEN_FILE is empty",
		},
	} {
		if _, err := tc.cfg.idToken(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, tc.want)
		}
	}

	// The same token in the variable and the file is no conflict.
	if token, err := (authConfig{oidcToken: "rotated", oidcTokenFiles: []tokenFile{{"ARM_OIDC_TOKEN_FILE_PATH", file}}}).idToken(); err != nil || token != "rotated" {
		t.Errorf("matching tokens: got %q, %v", token, err)
	}
}

func TestFetchADOIDToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oidc" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		query := r.URL.Query()
		for name, want := range map[string]string{
			"api-version":         "7.1",
			"serviceConnectionId": "connection",
			"audience":            oidcAudience,
			"kept":                "yes",
		} {
			if got := query.Get(name); got != want {
				t.Errorf("%s %q, want %q", name, got, want)
			}
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pipeline-token" {
			t.Errorf("Authorization %q", got)
		}
		_, _ = w.Write([]byte(`{"oidcToken":"ado-id-token"}`))
	}))
	defer server.Close()

	token, err := fetchADOIDToken(t.Context(), server.Client(), server.URL+"/oidc?kept=yes", "pipeline-token", "connection")
	if err != nil || token != "ado-id-token" {
		t.Errorf("got %q, %v", token, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer empty.Close()
	if _, err := fetchADOIDToken(t.Context(), empty.Client(), empty.URL, "pipeline-token", "connection"); err == nil || !strings.Contains(err.Error(), "without a token") {
		t.Errorf("an answer without a token: got %v", err)
	}
}

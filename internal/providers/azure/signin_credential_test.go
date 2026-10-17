// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

func TestSignInMethodOrder(t *testing.T) {
	// all has the settings of every method; each case takes some away.
	all := authConfig{
		tenantID:         "tenant",
		clientID:         "client",
		certificatePath:  "/certs/sp.pem",
		clientSecret:     "secret",
		useOIDC:          true,
		oidcRequestURL:   "https://oidc.example/token",
		oidcRequestToken: "request-token",
		useMSI:           true,
	}
	for name, tc := range map[string]struct {
		edit func(*authConfig)
		want signInMethod
	}{
		"a certificate wins over everything": {func(*authConfig) {}, signInWithCertificate},
		"then a secret":                      {func(c *authConfig) { c.certificatePath = "" }, signInWithSecret},
		"then OIDC": {func(c *authConfig) {
			c.certificatePath, c.clientSecret = "", ""
		}, signInWithOIDC},
		"OIDC needs its request URL": {func(c *authConfig) {
			c.certificatePath, c.clientSecret, c.oidcRequestURL = "", "", ""
		}, signInWithManagedIdentity},
		"OIDC needs its request token": {func(c *authConfig) {
			c.certificatePath, c.clientSecret, c.oidcRequestToken = "", "", ""
		}, signInWithManagedIdentity},
		"OIDC needs its flag": {func(c *authConfig) {
			c.certificatePath, c.clientSecret, c.useOIDC = "", "", false
		}, signInWithManagedIdentity},
		"then a managed identity": {func(c *authConfig) {
			c.certificatePath, c.clientSecret, c.useOIDC = "", "", false
		}, signInWithManagedIdentity},
		"and the Azure CLI when nothing else is set": {func(c *authConfig) {
			c.certificatePath, c.clientSecret, c.useOIDC, c.useMSI = "", "", false, false
		}, signInWithCLI},
		"the CLI with nothing at all": {func(c *authConfig) { *c = authConfig{} }, signInWithCLI},
	} {
		cfg := all
		tc.edit(&cfg)
		if got := cfg.method(); got != tc.want {
			t.Errorf("%s: got %s, want %s", name, got, tc.want)
		}
	}
}

// writeTestCertificate writes a self-signed certificate and its RSA key,
// in PEM, to a file and returns its path.
func writeTestCertificate(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "infraharvest test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})...)
	path := filepath.Join(t.TempDir(), "sp.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewCredentialTypes(t *testing.T) {
	// The managed identity credential reads where its endpoint is from the
	// hosting environment; keep a runner's own settings out of the test.
	for _, name := range []string{"IDENTITY_ENDPOINT", "IDENTITY_HEADER", "IDENTITY_SERVER_THUMBPRINT", "IMDS_ENDPOINT", "MSI_ENDPOINT", "MSI_SECRET"} {
		t.Setenv(name, "")
	}
	certificate := writeTestCertificate(t)
	custom := cloud.Configuration{
		ActiveDirectoryAuthorityHost: "https://login.stack.example/adfs/",
		Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			cloud.ResourceManager: {Endpoint: "https://management.stack.example/", Audience: "https://management.stack.example/"},
		},
	}
	application := authConfig{tenantID: "tenant", clientID: "client", auxiliaryTenants: []string{"aux1"}, cloud: cloud.AzurePublic}
	for name, tc := range map[string]struct {
		edit func(*authConfig)
		want string
	}{
		"certificate": {func(c *authConfig) { c.certificatePath = certificate }, "*azidentity.ClientCertificateCredential"},
		"certificate in a custom cloud": {func(c *authConfig) {
			c.certificatePath, c.cloud, c.metadataHost = certificate, custom, "management.stack.example"
		}, "*azidentity.ClientCertificateCredential"},
		"secret": {func(c *authConfig) { c.clientSecret = "secret" }, "*azidentity.ClientSecretCredential"},
		"secret in a custom cloud": {func(c *authConfig) {
			c.clientSecret, c.cloud, c.metadataHost = "secret", custom, "management.stack.example"
		}, "*azidentity.ClientSecretCredential"},
		"OIDC": {func(c *authConfig) {
			c.useOIDC, c.oidcRequestURL, c.oidcRequestToken = true, "https://oidc.example/token", "request-token"
		}, "*azidentity.ClientAssertionCredential"},
		"system-assigned managed identity": {func(c *authConfig) {
			c.useMSI, c.clientID = true, ""
		}, "*azidentity.ManagedIdentityCredential"},
		"user-assigned managed identity": {func(c *authConfig) { c.useMSI = true }, "*azidentity.ManagedIdentityCredential"},
		"managed identity through ARM_MSI_ENDPOINT": {func(c *authConfig) {
			c.useMSI, c.msiEndpoint = true, "https://management.stack.example/metadata/identity/oauth2/token"
		}, "cached *azure.msiEndpointCredential"},
		"Azure CLI": {func(*authConfig) {}, "cached *azidentity.AzureCLICredential"},
		"Azure CLI without a tenant": {func(c *authConfig) {
			c.tenantID, c.clientID = "", ""
		}, "cached *azidentity.AzureCLICredential"},
	} {
		cfg := application
		tc.edit(&cfg)
		credential, err := newCredential(cfg)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got := fmt.Sprintf("%T", credential)
		if cached, ok := credential.(*cachedCredential); ok {
			got = fmt.Sprintf("cached %T", cached.credential)
		}
		if got != tc.want {
			t.Errorf("%s: got a %s, want a %s", name, got, tc.want)
		}
	}
}

func TestNewCredentialPassesTheClientIDToTheMSIEndpoint(t *testing.T) {
	for clientID, want := range map[string]string{"": "", "client": "client"} {
		credential, err := newCredential(authConfig{useMSI: true, msiEndpoint: "https://msi.example/token", clientID: clientID})
		if err != nil {
			t.Fatal(err)
		}
		cached, ok := credential.(*cachedCredential)
		if !ok {
			t.Fatalf("got a %T, want its tokens cached", credential)
		}
		msi, ok := cached.credential.(*msiEndpointCredential)
		if !ok {
			t.Fatalf("got a %T", credential)
		}
		if msi.endpoint != "https://msi.example/token" || msi.clientID != want {
			t.Errorf("ARM_CLIENT_ID %q: endpoint %q, client ID %q", clientID, msi.endpoint, msi.clientID)
		}
		if msi.client == nil || msi.client.Timeout != time.Minute {
			t.Errorf("ARM_CLIENT_ID %q: the client has no one-minute timeout", clientID)
		}
	}
}

func TestNewCredentialNeedsTenantAndClient(t *testing.T) {
	methods := map[string]authConfig{
		"a client certificate": {certificatePath: "/certs/sp.pem"},
		"a client secret":      {clientSecret: "secret"},
		"OIDC":                 {useOIDC: true, oidcRequestURL: "https://oidc.example/token", oidcRequestToken: "request-token"},
	}
	for words, settings := range methods {
		for _, ids := range [][2]string{{"", "client"}, {"tenant", ""}, {"", ""}} {
			cfg := settings
			cfg.tenantID, cfg.clientID = ids[0], ids[1]
			_, err := newCredential(cfg)
			if err == nil {
				t.Errorf("%s with tenant %q and client %q: no error", words, ids[0], ids[1])
				continue
			}
			for _, part := range []string{"ARM_TENANT_ID", "ARM_CLIENT_ID", words} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("%s with tenant %q and client %q: %q doesn't say %q", words, ids[0], ids[1], err, part)
				}
			}
		}
	}
}

func TestNewCredentialCertificateErrors(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(t.TempDir(), "missing.pem"): "reading ARM_CLIENT_CERTIFICATE_PATH",
		garbage: "parsing the certificate",
	} {
		_, err := newCredential(authConfig{tenantID: "tenant", clientID: "client", certificatePath: path, cloud: cloud.AzurePublic})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error with %q", filepath.Base(path), err, want)
		}
	}
}

func TestFetchIDToken(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		// want are the query parameters the token endpoint must get.
		want map[string]string
	}{
		"Entra's audience added, the rest kept": {
			query: "?api-version=2.0",
			want:  map[string]string{"api-version": "2.0", "audience": "api://AzureADTokenExchange"},
		},
		"no query": {
			want: map[string]string{"audience": "api://AzureADTokenExchange"},
		},
		"an audience in the URL is kept": {
			query: "?api-version=2.0&audience=api://custom",
			want:  map[string]string{"api-version": "2.0", "audience": "api://custom"},
		},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/idtoken" {
				t.Errorf("%s: request %s %s", name, r.Method, r.URL)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer request-token" {
				t.Errorf("%s: Authorization %q", name, got)
			}
			if got := r.Header.Get("Accept"); got != "application/json" {
				t.Errorf("%s: Accept %q", name, got)
			}
			query := r.URL.Query()
			if len(query) != len(tc.want) {
				t.Errorf("%s: query %v, want %v", name, query, tc.want)
			}
			for k, v := range tc.want {
				if got := query[k]; len(got) != 1 || got[0] != v {
					t.Errorf("%s: %s = %q, want %q", name, k, got, v)
				}
			}
			_, _ = w.Write([]byte(`{"count":1,"value":"id-token"}`))
		}))
		token, err := fetchIDToken(t.Context(), server.Client(), server.URL+"/idtoken"+tc.query, "request-token")
		server.Close()
		if err != nil || token != "id-token" {
			t.Errorf("%s: got %q, %v; want the ID token", name, token, err)
		}
	}
}

func TestFetchIDTokenErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"unauthorized (401)": {http.StatusUnauthorized, `{"message":"bad token"}`},
		"no token":           {http.StatusOK, `{"value":""}`},
		"not JSON":           {http.StatusOK, `<html>`},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := fetchIDToken(t.Context(), server.Client(), server.URL, "request-token")
		server.Close()
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestMSIEndpointCredential(t *testing.T) {
	for _, clientID := range []string{"", "client"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/metadata/identity/oauth2/token" {
				t.Errorf("request %s %s", r.Method, r.URL)
			}
			if got := r.Header.Get("Metadata"); got != "true" {
				t.Errorf("Metadata header %q", got)
			}
			query := r.URL.Query()
			if got := query.Get("api-version"); got != "2018-02-01" {
				t.Errorf("api-version %q", got)
			}
			if got := query.Get("resource"); got != "https://management.stack.example/" {
				t.Errorf("resource %q, want the scope without /.default", got)
			}
			// client_id goes only with a client ID: without one the endpoint
			// answers for the system-assigned identity.
			if _, sent := query["client_id"]; sent != (clientID != "") || query.Get("client_id") != clientID {
				t.Errorf("client_id %q (sent %v), want %q", query.Get("client_id"), sent, clientID)
			}
			_, _ = w.Write([]byte(`{"access_token":"msi-token","expires_on":"1700000000","resource":"https://management.stack.example/","token_type":"Bearer"}`))
		}))
		credential := &msiEndpointCredential{endpoint: server.URL + "/metadata/identity/oauth2/token", clientID: clientID, client: server.Client()}
		token, err := credential.GetToken(t.Context(), policy.TokenRequestOptions{Scopes: []string{"https://management.stack.example//.default"}})
		server.Close()
		if err != nil {
			t.Errorf("client ID %q: %v", clientID, err)
			continue
		}
		if token.Token != "msi-token" || !token.ExpiresOn.Equal(time.Unix(1700000000, 0)) {
			t.Errorf("client ID %q: got %q expiring %v", clientID, token.Token, token.ExpiresOn)
		}
	}
}

func TestMSIEndpointCredentialErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}))
	defer server.Close()
	credential := &msiEndpointCredential{endpoint: server.URL, client: server.Client()}

	for name, scopes := range map[string][]string{
		"no scope":   nil,
		"two scopes": {"https://a/.default", "https://b/.default"},
	} {
		if _, err := credential.GetToken(t.Context(), policy.TokenRequestOptions{Scopes: scopes}); err == nil || !strings.Contains(err.Error(), "one scope") {
			t.Errorf("%s: got %v, want an error about one scope", name, err)
		}
	}
	if _, err := credential.GetToken(t.Context(), policy.TokenRequestOptions{Scopes: []string{"https://a/.default"}}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("a 400 answer: got %v, want an error with the status", err)
	}
}

func TestParseMSIToken(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for name, tc := range map[string]struct {
		body string
		want time.Time
	}{
		"expires_on as a number":       {`{"access_token":"t","expires_on":1700003600}`, time.Unix(1700003600, 0)},
		"expires_on as a string":       {`{"access_token":"t","expires_on":"1700003600"}`, time.Unix(1700003600, 0)},
		"expires_on wins":              {`{"access_token":"t","expires_on":"1700003600","expires_in":"60"}`, time.Unix(1700003600, 0)},
		"expires_in as a number":       {`{"access_token":"t","expires_in":3599}`, now.Add(3599 * time.Second)},
		"expires_in as a string":       {`{"access_token":"t","expires_in":"3599"}`, now.Add(3599 * time.Second)},
		"a null expires_on is skipped": {`{"access_token":"t","expires_on":null,"expires_in":60}`, now.Add(time.Minute)},
	} {
		token, err := parseMSIToken([]byte(tc.body), now)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if token.Token != "t" || !token.ExpiresOn.Equal(tc.want) {
			t.Errorf("%s: got %q expiring %v, want %v", name, token.Token, token.ExpiresOn, tc.want)
		}
	}

	for name, body := range map[string]string{
		"no access_token":          `{"expires_on":"1700003600"}`,
		"an empty access_token":    `{"access_token":"","expires_on":"1700003600"}`,
		"no expiry":                `{"access_token":"t"}`,
		"a non-numeric expiry":     `{"access_token":"t","expires_on":"soon"}`,
		"a non-numeric expires_in": `{"access_token":"t","expires_in":"soon"}`,
		"not JSON":                 `<html>`,
	} {
		if _, err := parseMSIToken([]byte(body), now); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

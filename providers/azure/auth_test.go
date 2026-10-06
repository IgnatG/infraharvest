// Copyright 2019 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package azure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

func envFrom(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestLoadAuthConfigNeedsSubscription(t *testing.T) {
	_, err := loadAuthConfig(envFrom(map[string]string{"ARM_CLIENT_ID": "client"}))
	if err == nil || !strings.Contains(err.Error(), "ARM_SUBSCRIPTION_ID") {
		t.Fatalf("err = %v, want one naming ARM_SUBSCRIPTION_ID", err)
	}
}

func TestLoadAuthConfigReadsARMVariables(t *testing.T) {
	cfg, err := loadAuthConfig(envFrom(map[string]string{
		"ARM_SUBSCRIPTION_ID":             "sub",
		"ARM_TENANT_ID":                   "tenant",
		"ARM_CLIENT_ID":                   "client",
		"ARM_CLIENT_SECRET":               "secret",
		"ARM_CLIENT_CERTIFICATE_PATH":     "/cert.pfx",
		"ARM_CLIENT_CERTIFICATE_PASSWORD": "pass",
		"ARM_USE_OIDC":                    "true",
		"ARM_OIDC_REQUEST_URL":            "https://token.example",
		"ARM_OIDC_REQUEST_TOKEN":          "request-token",
		"ARM_USE_MSI":                     "true",
		"ARM_MSI_ENDPOINT":                "http://msi.example",
		"ARM_AUXILIARY_TENANT_IDS":        "a;b",
		"ARM_ENVIRONMENT":                 "china",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := authConfig{
		subscriptionID:     "sub",
		tenantID:           "tenant",
		clientID:           "client",
		clientSecret:       "secret",
		clientCertPath:     "/cert.pfx",
		clientCertPassword: "pass",
		useOIDC:            true,
		oidcRequestURL:     "https://token.example",
		oidcRequestToken:   "request-token",
		useMSI:             true,
		msiEndpoint:        "http://msi.example",
		auxiliaryTenants:   []string{"a", "b"},
		cloud:              cloud.AzureChina,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %+v\nwant  %+v", cfg, want)
	}
}

func TestLoadAuthConfigFallsBackToGitHubActionsTokenVariables(t *testing.T) {
	cfg, err := loadAuthConfig(envFrom(map[string]string{
		"ARM_SUBSCRIPTION_ID":            "sub",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://actions.example",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "actions-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.oidcRequestURL != "https://actions.example" || cfg.oidcRequestToken != "actions-token" {
		t.Fatalf("OIDC request = %q, %q", cfg.oidcRequestURL, cfg.oidcRequestToken)
	}
}

func TestLoadAuthConfigRejectsMoreThanThreeAuxiliaryTenants(t *testing.T) {
	_, err := loadAuthConfig(envFrom(map[string]string{
		"ARM_SUBSCRIPTION_ID":      "sub",
		"ARM_AUXILIARY_TENANT_IDS": "a;b;c;d",
	}))
	if err == nil {
		t.Fatal("want an error for 4 auxiliary tenants")
	}
}

func TestCloudFromEnvironment(t *testing.T) {
	cases := map[string]cloud.Configuration{
		"":                       cloud.AzurePublic,
		"public":                 cloud.AzurePublic,
		"AzurePublicCloud":       cloud.AzurePublic,
		"usgovernment":           cloud.AzureGovernment,
		"AZUREUSGOVERNMENTCLOUD": cloud.AzureGovernment,
		"china":                  cloud.AzureChina,
		"AzureChinaCloud":        cloud.AzureChina,
	}
	for name, want := range cases {
		got, err := cloudFromEnvironment(name)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if got.ActiveDirectoryAuthorityHost != want.ActiveDirectoryAuthorityHost {
			t.Errorf("%q: authority %q, want %q", name, got.ActiveDirectoryAuthorityHost, want.ActiveDirectoryAuthorityHost)
		}
	}
	if _, err := cloudFromEnvironment("mars"); err == nil {
		t.Error("want an error for an unknown environment")
	}
}

func TestSelectAuthMethod(t *testing.T) {
	cases := []struct {
		name string
		cfg  authConfig
		want string
	}{
		{"nothing set", authConfig{}, authAzureCLI},
		{"certificate wins", authConfig{clientCertPath: "c", clientSecret: "s", useMSI: true}, authClientCertificate},
		{"secret", authConfig{clientSecret: "s", useMSI: true}, authClientSecret},
		{"oidc", authConfig{useOIDC: true, oidcRequestURL: "u", oidcRequestToken: "t", useMSI: true}, authOIDC},
		{"oidc without a request token", authConfig{useOIDC: true, oidcRequestURL: "u"}, authAzureCLI},
		{"managed identity", authConfig{useMSI: true}, authManagedIdentity},
	}
	for _, c := range cases {
		if got := selectAuthMethod(c.cfg); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestNewCredentialNeedsTenantAndClientForServicePrincipals(t *testing.T) {
	_, err := newCredential(authConfig{clientSecret: "s", cloud: cloud.AzurePublic})
	if err == nil || !strings.Contains(err.Error(), "ARM_TENANT_ID") {
		t.Fatalf("err = %v, want one naming ARM_TENANT_ID", err)
	}
}

func TestNewCredentialUsesTheCustomMSIEndpoint(t *testing.T) {
	cred, err := newCredential(authConfig{useMSI: true, msiEndpoint: "http://msi.example", clientID: "client"})
	if err != nil {
		t.Fatal(err)
	}
	msi, ok := cred.(*msiEndpointCredential)
	if !ok || msi.endpoint != "http://msi.example" || msi.clientID != "client" {
		t.Fatalf("credential = %#v", cred)
	}
}

func TestGitHubOIDCAssertion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer request-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("audience"); got != githubOIDCAudience {
			t.Errorf("audience = %q", got)
		}
		if got := r.URL.Query().Get("api-version"); got != "2.0" {
			t.Errorf("api-version = %q, want the request URL's query kept", got)
		}
		_, _ = w.Write([]byte(`{"count":1,"value":"id-token"}`))
	}))
	defer server.Close()

	token, err := githubOIDCAssertion(server.Client(), server.URL+"?api-version=2.0", "request-token")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "id-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestGitHubOIDCAssertionFailsOnErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := githubOIDCAssertion(server.Client(), server.URL, "t")(context.Background()); err == nil {
		t.Fatal("want an error for a 401")
	}
}

func TestMSIEndpointCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata") != "true" {
			t.Error("missing Metadata header")
		}
		q := r.URL.Query()
		if q.Get("resource") != "https://management.core.windows.net/" {
			t.Errorf("resource = %q", q.Get("resource"))
		}
		if q.Get("client_id") != "client" {
			t.Errorf("client_id = %q", q.Get("client_id"))
		}
		if q.Get("api-version") != "2018-02-01" {
			t.Errorf("api-version = %q", q.Get("api-version"))
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_on":"1700000000"}`))
	}))
	defer server.Close()

	cred := &msiEndpointCredential{endpoint: server.URL, clientID: "client", client: server.Client()}
	token, err := cred.GetToken(context.Background(), policy.TokenRequestOptions{
		Scopes: []string{"https://management.core.windows.net//.default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "tok" || !token.ExpiresOn.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("token = %+v", token)
	}
}

func TestParseMSIToken(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := map[string]time.Time{
		`{"access_token":"t","expires_on":1700000000}`:   time.Unix(1700000000, 0),
		`{"access_token":"t","expires_on":"1700000000"}`: time.Unix(1700000000, 0),
		`{"access_token":"t","expires_in":"3600"}`:       now.Add(time.Hour),
	}
	for body, want := range cases {
		token, err := parseMSIToken([]byte(body), now)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if !token.ExpiresOn.Equal(want) {
			t.Errorf("%s: expires %v, want %v", body, token.ExpiresOn, want)
		}
	}
	for _, body := range []string{`{"expires_on":1}`, `{"access_token":"t"}`, `not json`} {
		if _, err := parseMSIToken([]byte(body), now); err == nil {
			t.Errorf("%s: want an error", body)
		}
	}
}

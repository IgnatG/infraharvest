// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
)

// envOf is a getenv that reads vars; any other variable is unset.
func envOf(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadAuthConfigReadsTheEnvironment(t *testing.T) {
	cfg, err := loadAuthConfig(envOf(map[string]string{
		"ARM_SUBSCRIPTION_ID":             "sub",
		"ARM_TENANT_ID":                   "tenant",
		"ARM_CLIENT_ID":                   "client",
		"ARM_CLIENT_SECRET":               "secret",
		"ARM_CLIENT_CERTIFICATE_PATH":     "/certs/sp.pem",
		"ARM_CLIENT_CERTIFICATE_PASSWORD": "pass",
		"ARM_USE_OIDC":                    "true",
		"ARM_OIDC_REQUEST_URL":            "https://oidc.example/token",
		"ARM_OIDC_REQUEST_TOKEN":          "request-token",
		"ARM_USE_MSI":                     "true",
		"ARM_MSI_ENDPOINT":                "http://169.254.169.254/metadata/identity/oauth2/token",
		"ARM_AUXILIARY_TENANT_IDS":        "aux1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := authConfig{
		subscriptionID:      "sub",
		tenantID:            "tenant",
		clientID:            "client",
		clientSecret:        "secret",
		certificatePath:     "/certs/sp.pem",
		certificatePassword: "pass",
		useOIDC:             true,
		oidcRequestURL:      "https://oidc.example/token",
		oidcRequestToken:    "request-token",
		useMSI:              true,
		msiEndpoint:         "http://169.254.169.254/metadata/identity/oauth2/token",
		auxiliaryTenants:    []string{"aux1"},
		cloud:               cloud.AzurePublic,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestLoadAuthConfigWithNothingSet(t *testing.T) {
	cfg, err := loadAuthConfig(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	// No subscription is not an error here: Init and Subscriptions decide.
	if !reflect.DeepEqual(cfg, authConfig{cloud: cloud.AzurePublic}) {
		t.Errorf("got %+v, want only the public cloud", cfg)
	}
}

func TestLoadAuthConfigFallsBackToGitHubActions(t *testing.T) {
	actions := map[string]string{
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://actions.example/idtoken?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "actions-token",
	}
	cfg, err := loadAuthConfig(envOf(actions))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.oidcRequestURL != actions["ACTIONS_ID_TOKEN_REQUEST_URL"] || cfg.oidcRequestToken != "actions-token" {
		t.Errorf("without ARM_OIDC_*: got %q and %q", cfg.oidcRequestURL, cfg.oidcRequestToken)
	}

	// The ARM_ variables win over GitHub Actions' own.
	actions["ARM_OIDC_REQUEST_URL"] = "https://arm.example/token"
	actions["ARM_OIDC_REQUEST_TOKEN"] = "arm-token"
	cfg, err = loadAuthConfig(envOf(actions))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.oidcRequestURL != "https://arm.example/token" || cfg.oidcRequestToken != "arm-token" {
		t.Errorf("with ARM_OIDC_*: got %q and %q", cfg.oidcRequestURL, cfg.oidcRequestToken)
	}
}

// TestLoadAuthConfigFlags checks that ARM_USE_OIDC and ARM_USE_MSI are read
// as booleans, as the azurerm provider reads them: "false" and "0" turn the
// method off, and a value that is not a boolean is an error naming the
// variable and the value.
func TestLoadAuthConfigFlags(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{" true ", true}, // surrounding spaces are trimmed
		{"false", false},
		{"0", false},
		{"F", false},
	} {
		value, want := tc.value, tc.want
		cfg, err := loadAuthConfig(envOf(map[string]string{"ARM_USE_OIDC": value, "ARM_USE_MSI": value}))
		if err != nil {
			t.Errorf("%q: %v", value, err)
			continue
		}
		if cfg.useOIDC != want || cfg.useMSI != want {
			t.Errorf("%q: useOIDC %v, useMSI %v, want both %v", value, cfg.useOIDC, cfg.useMSI, want)
		}
	}

	for _, name := range []string{"ARM_USE_OIDC", "ARM_USE_MSI"} {
		for _, value := range []string{"yes", "no", "garbage"} {
			_, err := loadAuthConfig(envOf(map[string]string{name: value}))
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), strconv.Quote(value)) {
				t.Errorf("%s=%q: got %v, want an error naming the variable and the value", name, value, err)
			}
		}
	}
}

func TestLoadAuthConfigAuxiliaryTenants(t *testing.T) {
	cfg, err := loadAuthConfig(envOf(map[string]string{"ARM_AUXILIARY_TENANT_IDS": " aux1 ;; aux2;aux3 ; "}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"aux1", "aux2", "aux3"}; !reflect.DeepEqual(cfg.auxiliaryTenants, want) {
		t.Errorf("got %q, want %q", cfg.auxiliaryTenants, want)
	}

	_, err = loadAuthConfig(envOf(map[string]string{"ARM_AUXILIARY_TENANT_IDS": "a;b;c;d"}))
	if err == nil || !strings.Contains(err.Error(), "only 3") {
		t.Errorf("four auxiliary tenants: got %v, want an error saying only 3 are supported", err)
	}
}

func TestLoadAuthConfigEnvironments(t *testing.T) {
	for environment, want := range map[string]cloud.Configuration{
		"":                     cloud.AzurePublic,
		"public":               cloud.AzurePublic,
		"global":               cloud.AzurePublic,
		"AzurePublicCloud":     cloud.AzurePublic,
		"AzureCloud":           cloud.AzurePublic,
		"  Public  ":           cloud.AzurePublic,
		"usgovernment":         cloud.AzureGovernment,
		"USGovernmentL4":       cloud.AzureGovernment,
		"usgovernmentl5":       cloud.AzureGovernment,
		"dod":                  cloud.AzureGovernment,
		"AzureUSGovernment":    cloud.AzureGovernment,
		"china":                cloud.AzureChina,
		"AzureChinaCloud":      cloud.AzureChina,
		"azurechina":           cloud.AzureChina,
		"AzureUSGovernmentL4C": {}, // unknown
		"germany":              {}, // unknown: Azure Germany is closed
	} {
		cfg, err := loadAuthConfig(envOf(map[string]string{"ARM_ENVIRONMENT": environment}))
		if want.ActiveDirectoryAuthorityHost == "" {
			if err == nil || !strings.Contains(err.Error(), "public, usgovernment, china or stack") {
				t.Errorf("%q: got %v, want an error listing the accepted values", environment, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", environment, err)
			continue
		}
		if !reflect.DeepEqual(cfg.cloud, want) {
			t.Errorf("%q: got the cloud with authority %s, want %s", environment, cfg.cloud.ActiveDirectoryAuthorityHost, want.ActiveDirectoryAuthorityHost)
		}
		if cfg.metadataHost != "" {
			t.Errorf("%q: metadata host %q, want none", environment, cfg.metadataHost)
		}
	}
}

func TestLoadAuthConfigStack(t *testing.T) {
	_, err := loadAuthConfig(envOf(map[string]string{"ARM_ENVIRONMENT": "stack"}))
	if err == nil || !strings.Contains(err.Error(), "ARM_METADATA_HOSTNAME") {
		t.Errorf("stack without a host: got %v, want an error naming ARM_METADATA_HOSTNAME", err)
	}

	cfg, err := loadAuthConfig(envOf(map[string]string{"ARM_ENVIRONMENT": " Stack ", "ARM_METADATA_HOSTNAME": "management.local.azurestack.external"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.metadataHost != "management.local.azurestack.external" {
		t.Errorf("metadata host %q", cfg.metadataHost)
	}
	// signIn reads the cloud from the host; until then it is unset.
	if !reflect.DeepEqual(cfg.cloud, cloud.Configuration{}) {
		t.Errorf("cloud %+v, want it left for signIn", cfg.cloud)
	}
}

// metadataServer serves body with status at the metadata endpoints path,
// checking the request the way an Azure metadata host would see it, and
// returns the server and its host.
func metadataServer(t *testing.T, status int, body string) (server *httptest.Server, host string) {
	t.Helper()
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/metadata/endpoints" || r.URL.Query().Get("api-version") != "2022-09-01" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept %q", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, strings.TrimPrefix(server.URL, "https://")
}

func TestCloudFromMetadata(t *testing.T) {
	const signIn = `"authentication":{"loginEndpoint":"https://login.stack.example/adfs","audiences":["https://management.stack.example/123","https://other"]}`
	for name, tc := range map[string]struct {
		body string
		// resourceManager is the endpoint wanted; empty means the host.
		resourceManager string
	}{
		"one object (Azure Stack Hub with resourceManager, emulators)": {
			body:            `{"resourceManager":"https://management.stack.example/",` + signIn + `}`,
			resourceManager: "https://management.stack.example/",
		},
		"a list of one (Azure's own answer)": {
			body:            `[{"name":"AzureCloud","resourceManager":"https://management.stack.example/",` + signIn + `}]`,
			resourceManager: "https://management.stack.example/",
		},
		"no resourceManager (Azure Stack Hub)": {
			body: `{` + signIn + `}`,
		},
	} {
		server, host := metadataServer(t, http.StatusOK, tc.body)
		got, err := cloudFromMetadata(t.Context(), server.Client(), host)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		resourceManager := tc.resourceManager
		if resourceManager == "" {
			resourceManager = "https://" + host
		}
		want := cloud.Configuration{
			ActiveDirectoryAuthorityHost: "https://login.stack.example/adfs",
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {Endpoint: resourceManager, Audience: "https://management.stack.example/123"},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
}

func TestCloudFromMetadataErrors(t *testing.T) {
	const entry = `{"resourceManager":"https://arm.example/","authentication":{"loginEndpoint":"https://login.example/","audiences":["https://arm.example/"]}}`
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"two clouds":         {http.StatusOK, "[" + entry + "," + entry + "]", "list of 2"},
		"an empty list":      {http.StatusOK, "[]", "list of 0"},
		"no sign-in data":    {http.StatusOK, `{"resourceManager":"https://arm.example/"}`, "no sign-in data"},
		"no audience":        {http.StatusOK, `{"authentication":{"loginEndpoint":"https://login.example/","audiences":[]}}`, "no sign-in data"},
		"no login endpoint":  {http.StatusOK, `{"authentication":{"audiences":["https://arm.example/"]}}`, "no sign-in data"},
		"not JSON":           {http.StatusOK, `<html>`, "endpoints of"},
		"not found":          {http.StatusNotFound, `{}`, "reading the endpoints"},
		"server error":       {http.StatusInternalServerError, entry, "reading the endpoints"},
		"unauthorized (401)": {http.StatusUnauthorized, entry, "reading the endpoints"},
	} {
		server, host := metadataServer(t, tc.status, tc.body)
		_, err := cloudFromMetadata(t.Context(), server.Client(), host)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, tc.want)
		}
	}
}

func TestCloudFromMetadataUnreachable(t *testing.T) {
	server, host := metadataServer(t, http.StatusOK, "{}")
	server.Close()
	_, err := cloudFromMetadata(t.Context(), server.Client(), host)
	if err == nil || !strings.Contains(err.Error(), "reading the endpoints") {
		t.Errorf("got %v, want an error about reading the endpoints", err)
	}
}

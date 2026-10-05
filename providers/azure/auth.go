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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Ways of signing in, picked by selectAuthMethod in the same order the
// azurerm provider (and go-azure-helpers, which this provider used before)
// tries them.
const (
	authClientCertificate = "client_certificate"
	authClientSecret      = "client_secret"
	authOIDC              = "oidc"
	authManagedIdentity   = "managed_identity"
	authAzureCLI          = "azure_cli"
)

// githubOIDCAudience is the audience Microsoft Entra ID expects in a GitHub
// Actions ID token exchanged for an access token.
const githubOIDCAudience = "api://AzureADTokenExchange"

// authConfig is the sign-in configuration read from the ARM_* environment
// variables the azurerm provider documents.
type authConfig struct {
	subscriptionID     string
	tenantID           string
	clientID           string
	clientSecret       string
	clientCertPath     string
	clientCertPassword string
	useOIDC            bool
	oidcRequestURL     string
	oidcRequestToken   string
	useMSI             bool
	msiEndpoint        string
	auxiliaryTenants   []string
	cloud              cloud.Configuration
}

// loadAuthConfig reads the sign-in configuration through getenv (os.Getenv
// outside tests).
func loadAuthConfig(getenv func(string) string) (authConfig, error) {
	cfg := authConfig{
		subscriptionID:     getenv("ARM_SUBSCRIPTION_ID"),
		tenantID:           getenv("ARM_TENANT_ID"),
		clientID:           getenv("ARM_CLIENT_ID"),
		clientSecret:       getenv("ARM_CLIENT_SECRET"),
		clientCertPath:     getenv("ARM_CLIENT_CERTIFICATE_PATH"),
		clientCertPassword: getenv("ARM_CLIENT_CERTIFICATE_PASSWORD"),
		useOIDC:            getenv("ARM_USE_OIDC") != "",
		oidcRequestURL:     firstNonEmpty(getenv("ARM_OIDC_REQUEST_URL"), getenv("ACTIONS_ID_TOKEN_REQUEST_URL")),
		oidcRequestToken:   firstNonEmpty(getenv("ARM_OIDC_REQUEST_TOKEN"), getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")),
		useMSI:             getenv("ARM_USE_MSI") != "",
		msiEndpoint:        getenv("ARM_MSI_ENDPOINT"),
	}
	if cfg.subscriptionID == "" {
		return authConfig{}, errors.New("set ARM_SUBSCRIPTION_ID env var")
	}
	if v := getenv("ARM_AUXILIARY_TENANT_IDS"); v != "" {
		for _, tenant := range strings.Split(v, ";") {
			if tenant = strings.TrimSpace(tenant); tenant != "" {
				cfg.auxiliaryTenants = append(cfg.auxiliaryTenants, tenant)
			}
		}
		if len(cfg.auxiliaryTenants) > 3 {
			return authConfig{}, errors.New("the provider only supports 3 auxiliary tenant IDs for ARM_AUXILIARY_TENANT_IDS")
		}
	}
	c, err := cloudFromEnvironment(getenv("ARM_ENVIRONMENT"))
	if err != nil {
		return authConfig{}, err
	}
	cfg.cloud = c
	return cfg, nil
}

// cloudFromEnvironment maps ARM_ENVIRONMENT to an Azure cloud. It accepts the
// short names the azurerm provider uses (public, usgovernment, china) and the
// AZURE<NAME>CLOUD spelling; empty means the public cloud.
func cloudFromEnvironment(name string) (cloud.Configuration, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(strings.TrimPrefix(n, "azure"), "cloud")
	switch n {
	case "", "public", "global":
		return cloud.AzurePublic, nil
	case "usgovernment", "usgovernmentl4", "usgovernmentl5", "dod":
		return cloud.AzureGovernment, nil
	case "china":
		return cloud.AzureChina, nil
	}
	return cloud.Configuration{}, fmt.Errorf("unsupported ARM_ENVIRONMENT %q: use public, usgovernment or china", name)
}

// selectAuthMethod picks how to sign in: a client certificate, a client
// secret, a GitHub Actions OIDC token, a managed identity, and otherwise the
// Azure CLI login.
func selectAuthMethod(cfg authConfig) string {
	switch {
	case cfg.clientCertPath != "":
		return authClientCertificate
	case cfg.clientSecret != "":
		return authClientSecret
	case cfg.useOIDC && cfg.oidcRequestURL != "" && cfg.oidcRequestToken != "":
		return authOIDC
	case cfg.useMSI:
		return authManagedIdentity
	default:
		return authAzureCLI
	}
}

// newCredential builds the credential for the sign-in method cfg selects.
func newCredential(cfg authConfig) (azcore.TokenCredential, error) {
	clientOptions := azcore.ClientOptions{Cloud: cfg.cloud}
	method := selectAuthMethod(cfg)
	switch method {
	case authClientCertificate, authClientSecret, authOIDC:
		if cfg.tenantID == "" || cfg.clientID == "" {
			return nil, fmt.Errorf("signing in with %s needs ARM_TENANT_ID and ARM_CLIENT_ID", strings.ReplaceAll(method, "_", " "))
		}
	}
	switch method {
	case authClientCertificate:
		data, err := os.ReadFile(cfg.clientCertPath)
		if err != nil {
			return nil, fmt.Errorf("reading ARM_CLIENT_CERTIFICATE_PATH: %w", err)
		}
		var password []byte
		if cfg.clientCertPassword != "" {
			password = []byte(cfg.clientCertPassword)
		}
		certs, key, err := azidentity.ParseCertificates(data, password)
		if err != nil {
			return nil, fmt.Errorf("parsing the client certificate: %w", err)
		}
		return azidentity.NewClientCertificateCredential(cfg.tenantID, cfg.clientID, certs, key,
			&azidentity.ClientCertificateCredentialOptions{
				ClientOptions:              clientOptions,
				AdditionallyAllowedTenants: cfg.auxiliaryTenants,
			})
	case authClientSecret:
		return azidentity.NewClientSecretCredential(cfg.tenantID, cfg.clientID, cfg.clientSecret,
			&azidentity.ClientSecretCredentialOptions{
				ClientOptions:              clientOptions,
				AdditionallyAllowedTenants: cfg.auxiliaryTenants,
			})
	case authOIDC:
		httpClient := &http.Client{Timeout: time.Minute}
		return azidentity.NewClientAssertionCredential(cfg.tenantID, cfg.clientID,
			githubOIDCAssertion(httpClient, cfg.oidcRequestURL, cfg.oidcRequestToken),
			&azidentity.ClientAssertionCredentialOptions{
				ClientOptions:              clientOptions,
				AdditionallyAllowedTenants: cfg.auxiliaryTenants,
			})
	case authManagedIdentity:
		if cfg.msiEndpoint != "" {
			return &msiEndpointCredential{
				endpoint: cfg.msiEndpoint,
				clientID: cfg.clientID,
				client:   &http.Client{Timeout: time.Minute},
			}, nil
		}
		options := &azidentity.ManagedIdentityCredentialOptions{ClientOptions: clientOptions}
		if cfg.clientID != "" {
			options.ID = azidentity.ClientID(cfg.clientID)
		}
		return azidentity.NewManagedIdentityCredential(options)
	default:
		return azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{
			TenantID:                   cfg.tenantID,
			AdditionallyAllowedTenants: cfg.auxiliaryTenants,
		})
	}
}

// githubOIDCAssertion returns a callback that fetches a GitHub Actions ID
// token for Microsoft Entra ID workload identity federation.
func githubOIDCAssertion(client *http.Client, requestURL, requestToken string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		u, err := url.Parse(requestURL)
		if err != nil {
			return "", fmt.Errorf("parsing the OIDC request URL: %w", err)
		}
		query := u.Query()
		if query.Get("audience") == "" {
			query.Set("audience", githubOIDCAudience)
			u.RawQuery = query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+requestToken)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("requesting the OIDC token: %w", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return "", fmt.Errorf("reading the OIDC token: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("requesting the OIDC token: %s", resp.Status)
		}
		var token struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(body, &token); err != nil {
			return "", fmt.Errorf("decoding the OIDC token: %w", err)
		}
		if token.Value == "" {
			return "", errors.New("the OIDC token response has no token")
		}
		return token.Value, nil
	}
}

// msiEndpointCredential gets managed identity tokens from a custom endpoint
// (ARM_MSI_ENDPOINT); azidentity always talks to the well-known one.
type msiEndpointCredential struct {
	endpoint string
	clientID string
	client   *http.Client
}

func (c *msiEndpointCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if len(opts.Scopes) != 1 {
		return azcore.AccessToken{}, errors.New("managed identity tokens need exactly one scope")
	}
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("parsing ARM_MSI_ENDPOINT: %w", err)
	}
	query := u.Query()
	query.Set("api-version", "2018-02-01")
	// Managed identity endpoints take a resource (audience), not a scope.
	query.Set("resource", strings.TrimSuffix(opts.Scopes[0], "/.default"))
	if c.clientID != "" {
		query.Set("client_id", c.clientID)
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return azcore.AccessToken{}, err
	}
	req.Header.Set("Metadata", "true")
	resp, err := c.client.Do(req)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("requesting a managed identity token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("reading the managed identity token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return azcore.AccessToken{}, fmt.Errorf("requesting a managed identity token: %s", resp.Status)
	}
	return parseMSIToken(body, time.Now())
}

// parseMSIToken decodes a managed identity token response. expires_on (Unix
// seconds) and expires_in come as strings or numbers depending on the host.
func parseMSIToken(body []byte, now time.Time) (azcore.AccessToken, error) {
	var token struct {
		AccessToken string          `json:"access_token"`
		ExpiresOn   json.RawMessage `json:"expires_on"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return azcore.AccessToken{}, fmt.Errorf("decoding the managed identity token: %w", err)
	}
	if token.AccessToken == "" {
		return azcore.AccessToken{}, errors.New("the managed identity token response has no token")
	}
	if seconds, ok := jsonSeconds(token.ExpiresOn); ok {
		return azcore.AccessToken{Token: token.AccessToken, ExpiresOn: time.Unix(seconds, 0)}, nil
	}
	if seconds, ok := jsonSeconds(token.ExpiresIn); ok {
		return azcore.AccessToken{Token: token.AccessToken, ExpiresOn: now.Add(time.Duration(seconds) * time.Second)}, nil
	}
	return azcore.AccessToken{}, errors.New("the managed identity token response has no expiry")
}

func jsonSeconds(raw json.RawMessage) (int64, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

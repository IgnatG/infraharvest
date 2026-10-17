// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

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
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// oidcAudience is the audience Entra expects of a federated ID token, which
// GitHub Actions puts in the token when the request asks for it.
const oidcAudience = "api://AzureADTokenExchange"

// adoOIDCAPIVersion is the Azure DevOps API version an Azure Pipelines
// OIDC token is asked for with, the one azurerm asks with.
const adoOIDCAPIVersion = "7.1"

// msiAPIVersion is the managed identity endpoint API version that
// ARM_MSI_ENDPOINT is called with, the one Azure's instance metadata service
// and emulators such as floci-az answer.
const msiAPIVersion = "2018-02-01"

// signInHTTPTimeout bounds each request infraharvest itself sends to sign in
// (an ID token, a managed identity token), so a dead endpoint fails the
// sign-in instead of hanging it.
const signInHTTPTimeout = time.Minute

// signInMethod is a way to sign in to Azure.
type signInMethod int

const (
	signInWithCLI signInMethod = iota
	signInWithCertificate
	signInWithSecret
	signInWithOIDCToken
	signInWithADOPipeline
	signInWithOIDC
	signInWithManagedIdentity
	// signInWithNothing is no method at all: the Azure CLI is turned off
	// (ARM_USE_CLI=false) and nothing else is set up.
	signInWithNothing
)

// String names the method in words, for error messages.
func (m signInMethod) String() string {
	switch m {
	case signInWithCertificate:
		return "a client certificate"
	case signInWithSecret:
		return "a client secret"
	case signInWithOIDCToken:
		return "an OIDC token"
	case signInWithADOPipeline:
		return "Azure DevOps pipeline OIDC"
	case signInWithOIDC:
		return "OIDC"
	case signInWithManagedIdentity:
		return "a managed identity"
	case signInWithNothing:
		return "no method"
	}
	return "the Azure CLI"
}

// needsApplication says whether the method signs in as an application
// registration, which takes both a tenant and a client ID.
func (m signInMethod) needsApplication() bool {
	switch m {
	case signInWithCertificate, signInWithSecret, signInWithOIDCToken, signInWithADOPipeline, signInWithOIDC:
		return true
	}
	return false
}

// method picks how cfg signs in: the first method, in the order azurerm
// tries them, whose settings are all present, and the Azure CLI when none
// is, unless ARM_USE_CLI turns it off.
func (cfg authConfig) method() signInMethod {
	requested := cfg.oidcRequestURL != "" && cfg.oidcRequestToken != ""
	switch {
	case len(cfg.certificateData) > 0 || cfg.certificatePath != "":
		return signInWithCertificate
	case cfg.clientSecret != "":
		return signInWithSecret
	case cfg.useOIDC && (cfg.oidcToken != "" || len(cfg.oidcTokenFiles) > 0):
		return signInWithOIDCToken
	case cfg.useOIDC && requested && cfg.adoServiceConnectionID != "":
		return signInWithADOPipeline
	case cfg.useOIDC && requested:
		return signInWithOIDC
	case cfg.useMSI:
		return signInWithManagedIdentity
	case cfg.cliDisabled:
		return signInWithNothing
	}
	return signInWithCLI
}

// errNoIDToken is the error of ARM_USE_OIDC (or AKS workload identity)
// with no ID token to sign in with and no managed identity to use instead.
var errNoIDToken = errors.New("ARM_USE_OIDC (or ARM_USE_AKS_WORKLOAD_IDENTITY) is set, but there is no ID token to sign in with:" +
	" set ARM_OIDC_TOKEN, ARM_OIDC_TOKEN_FILE_PATH, or ARM_OIDC_REQUEST_URL and ARM_OIDC_REQUEST_TOKEN" +
	" (in GitHub Actions, grant the job id-token: write; on AKS, enable workload identity for the pod)")

// errNoSignIn is the error of ARM_USE_CLI=false with no other method set
// up.
var errNoSignIn = errors.New("ARM_USE_CLI is false and no other way to sign in to Azure is set up:" +
	" set up a client certificate, a client secret, OIDC or a managed identity, as for the azurerm provider")

// newCredential builds the credential cfg's method signs in with, for cfg's
// cloud. It sends no request: the credential signs in on its first token.
// OIDC asked for without an ID token is an error rather than a quiet fall
// back to the Azure CLI, which would list as another identity than the
// one meant, as is ARM_USE_CLI=false with nothing else set up.
func newCredential(cfg authConfig) (azcore.TokenCredential, error) {
	method := cfg.method()
	switch {
	case cfg.useOIDC && (method == signInWithCLI || method == signInWithNothing):
		return nil, errNoIDToken
	case method == signInWithNothing:
		return nil, errNoSignIn
	case method.needsApplication() && (cfg.tenantID == "" || cfg.clientID == ""):
		return nil, fmt.Errorf("ARM_TENANT_ID and ARM_CLIENT_ID are both required to sign in with %s", method)
	}
	options := azcore.ClientOptions{Cloud: cfg.cloud}
	// A custom cloud's authority is not one Entra's instance discovery
	// knows, so the credentials must take it as it is.
	customAuthority := cfg.metadataHost != ""
	assertionOptions := &azidentity.ClientAssertionCredentialOptions{
		ClientOptions:              options,
		AdditionallyAllowedTenants: cfg.auxiliaryTenants,
		DisableInstanceDiscovery:   customAuthority,
	}

	switch method {
	case signInWithCertificate:
		return certificateCredential(cfg, options, customAuthority)
	case signInWithSecret:
		return asCredential(azidentity.NewClientSecretCredential(cfg.tenantID, cfg.clientID, cfg.clientSecret,
			&azidentity.ClientSecretCredentialOptions{
				ClientOptions:              options,
				AdditionallyAllowedTenants: cfg.auxiliaryTenants,
				DisableInstanceDiscovery:   customAuthority,
			}))
	case signInWithOIDCToken:
		assertion := func(context.Context) (string, error) { return cfg.idToken() }
		return asCredential(azidentity.NewClientAssertionCredential(cfg.tenantID, cfg.clientID, assertion, assertionOptions))
	case signInWithADOPipeline:
		client := &http.Client{Timeout: signInHTTPTimeout}
		assertion := func(ctx context.Context) (string, error) {
			return fetchADOIDToken(ctx, client, cfg.oidcRequestURL, cfg.oidcRequestToken, cfg.adoServiceConnectionID)
		}
		return asCredential(azidentity.NewClientAssertionCredential(cfg.tenantID, cfg.clientID, assertion, assertionOptions))
	case signInWithOIDC:
		client := &http.Client{Timeout: signInHTTPTimeout}
		assertion := func(ctx context.Context) (string, error) {
			return fetchIDToken(ctx, client, cfg.oidcRequestURL, cfg.oidcRequestToken)
		}
		return asCredential(azidentity.NewClientAssertionCredential(cfg.tenantID, cfg.clientID, assertion, assertionOptions))
	case signInWithManagedIdentity:
		if cfg.msiEndpoint != "" {
			return cacheTokens(&msiEndpointCredential{
				endpoint:   cfg.msiEndpoint,
				apiVersion: cfg.msiAPIVersion,
				clientID:   cfg.clientID,
				client:     &http.Client{Timeout: signInHTTPTimeout},
			}), nil
		}
		msiOptions := &azidentity.ManagedIdentityCredentialOptions{ClientOptions: options}
		if cfg.clientID != "" {
			// A client ID picks a user-assigned identity; without one
			// the system-assigned identity signs in.
			msiOptions.ID = azidentity.ClientID(cfg.clientID)
		}
		return asCredential(azidentity.NewManagedIdentityCredential(msiOptions))
	}
	// The Azure CLI and ARM_MSI_ENDPOINT credentials keep no tokens, unlike
	// azidentity's others, so they are kept here (see cachedCredential).
	cli, err := azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{
		TenantID:                   cfg.tenantID,
		AdditionallyAllowedTenants: cfg.auxiliaryTenants,
	})
	if err != nil {
		return nil, err
	}
	return cacheTokens(cli), nil
}

// idToken returns the OIDC ID token cfg holds: ARM_OIDC_TOKEN, or the
// trimmed content of the files of oidcTokenFiles, read now, as AKS
// rotates its token. Several of them must hold the same token, as azurerm
// requires.
func (cfg authConfig) idToken() (string, error) {
	token, from := cfg.oidcToken, "ARM_OIDC_TOKEN"
	for _, f := range cfg.oidcTokenFiles {
		data, err := os.ReadFile(f.path)
		if err != nil {
			return "", fmt.Errorf("reading the OIDC token in %s: %w", f.variable, err)
		}
		fileToken := strings.TrimSpace(string(data))
		if token != "" && token != fileToken {
			return "", fmt.Errorf("%s and %s hold different OIDC tokens: unset one or make them match", from, f.variable)
		}
		token, from = fileToken, f.variable
	}
	if token == "" {
		return "", fmt.Errorf("the OIDC token in %s is empty", from)
	}
	return token, nil
}

// asCredential returns a constructor's credential as an interface, and a
// nil interface (not a typed nil pointer) when the constructor failed.
func asCredential[C azcore.TokenCredential](credential C, err error) (azcore.TokenCredential, error) {
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// certificateCredential signs in with the certificate (and its private
// key) ARM_CLIENT_CERTIFICATE holds or, without it, that is in the file
// ARM_CLIENT_CERTIFICATE_PATH names, as azurerm picks them.
func certificateCredential(cfg authConfig, options azcore.ClientOptions, customAuthority bool) (azcore.TokenCredential, error) {
	data, from := cfg.certificateData, "ARM_CLIENT_CERTIFICATE"
	if len(data) == 0 {
		from = "ARM_CLIENT_CERTIFICATE_PATH"
		var err error
		if data, err = os.ReadFile(cfg.certificatePath); err != nil {
			return nil, fmt.Errorf("reading ARM_CLIENT_CERTIFICATE_PATH: %w", err)
		}
	}
	var password []byte
	if cfg.certificatePassword != "" {
		password = []byte(cfg.certificatePassword)
	}
	certificates, key, err := azidentity.ParseCertificates(data, password)
	if err != nil {
		return nil, fmt.Errorf("parsing the certificate in %s: %w", from, err)
	}
	return asCredential(azidentity.NewClientCertificateCredential(cfg.tenantID, cfg.clientID, certificates, key,
		&azidentity.ClientCertificateCredentialOptions{
			ClientOptions:              options,
			AdditionallyAllowedTenants: cfg.auxiliaryTenants,
			DisableInstanceDiscovery:   customAuthority,
		}))
}

// fetchIDToken asks the OIDC request URL (GitHub Actions' ID token endpoint,
// or one that answers the same way) for an ID token to exchange for an
// Entra token. It asks for Entra's audience unless the URL names one.
func fetchIDToken(ctx context.Context, client *http.Client, requestURL, requestToken string) (string, error) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("the OIDC request URL: %w", err)
	}
	query := u.Query()
	if !query.Has("audience") {
		query.Set("audience", oidcAudience)
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return "", fmt.Errorf("requesting an OIDC ID token: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+requestToken)
	body, err := doSignInRequest(client, req)
	if err != nil {
		return "", fmt.Errorf("requesting an OIDC ID token: %w", err)
	}
	var answer struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("decoding the OIDC ID token: %w", err)
	}
	if answer.Value == "" {
		return "", errors.New("the OIDC request URL answered without an ID token")
	}
	return answer.Value, nil
}

// fetchADOIDToken asks an Azure DevOps pipeline's OIDC request URL
// (SYSTEM_OIDCREQUESTURI) for the ID token of the service connection
// serviceConnectionID, the way azurerm does: a POST with the API version,
// the service connection and Entra's audience, unless the URL names them.
func fetchADOIDToken(ctx context.Context, client *http.Client, requestURL, requestToken, serviceConnectionID string) (string, error) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("the OIDC request URL: %w", err)
	}
	query := u.Query()
	for name, value := range map[string]string{
		"api-version":         adoOIDCAPIVersion,
		"serviceConnectionId": serviceConnectionID,
		"audience":            oidcAudience,
	} {
		if !query.Has(name) {
			query.Set(name, value)
		}
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), http.NoBody)
	if err != nil {
		return "", fmt.Errorf("requesting an Azure DevOps OIDC token: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+requestToken)
	body, err := doSignInRequest(client, req)
	if err != nil {
		return "", fmt.Errorf("requesting an Azure DevOps OIDC token: %w", err)
	}
	var answer struct {
		OIDCToken string `json:"oidcToken"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("decoding the Azure DevOps OIDC token: %w", err)
	}
	if answer.OIDCToken == "" {
		return "", errors.New("the Azure DevOps OIDC request URL answered without a token")
	}
	return answer.OIDCToken, nil
}

// doSignInRequest sends req and returns at most maxSignInResponse bytes of
// the body of a 200 answer; any other status is an error.
func doSignInRequest(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSignInResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", req.URL.Redacted(), resp.Status)
	}
	return body, nil
}

// msiEndpointCredential gets managed identity tokens from the endpoint
// ARM_MSI_ENDPOINT names, the way Azure's instance metadata service hands
// them out. azidentity only calls the endpoints a hosting environment
// advertises, and Azure Stack Hub or an emulator such as floci-az has its
// own.
type msiEndpointCredential struct {
	endpoint string
	// apiVersion is ARM_MSI_API_VERSION, the endpoint's API version to
	// ask with; empty, it is msiAPIVersion.
	apiVersion string
	// clientID picks a user-assigned identity; empty, the endpoint
	// answers for the system-assigned one.
	clientID string
	client   *http.Client
}

// GetToken asks the endpoint for a token for the one resource the scope
// names. newCredential keeps its tokens (see cachedCredential): azcore
// keeps them only in each client's bearer token policy.
func (c *msiEndpointCredential) GetToken(ctx context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if len(options.Scopes) != 1 {
		return azcore.AccessToken{}, fmt.Errorf("a managed identity token is for one scope, not %d", len(options.Scopes))
	}
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("ARM_MSI_ENDPOINT: %w", err)
	}
	query := u.Query()
	apiVersion := c.apiVersion
	if apiVersion == "" {
		apiVersion = msiAPIVersion
	}
	query.Set("api-version", apiVersion)
	// The endpoint takes a resource, which is the scope without the
	// "/.default" that marks a resource's default permissions.
	query.Set("resource", strings.TrimSuffix(options.Scopes[0], "/.default"))
	if c.clientID != "" {
		query.Set("client_id", c.clientID)
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("requesting a managed identity token: %w", err)
	}
	req.Header.Set("Metadata", "true")
	body, err := doSignInRequest(c.client, req)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("requesting a managed identity token: %w", err)
	}
	return parseMSIToken(body, time.Now())
}

// parseMSIToken decodes a managed identity endpoint's answer. The token
// expires at expires_on (Unix seconds) or, failing that, expires_in seconds
// after now; endpoints send either as a JSON number or a numeric string.
func parseMSIToken(body []byte, now time.Time) (azcore.AccessToken, error) {
	var answer struct {
		AccessToken string          `json:"access_token"`
		ExpiresOn   json.RawMessage `json:"expires_on"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return azcore.AccessToken{}, fmt.Errorf("decoding the managed identity token: %w", err)
	}
	if answer.AccessToken == "" {
		return azcore.AccessToken{}, errors.New("the managed identity endpoint answered without an access_token")
	}
	seconds, ok, err := jsonSeconds(answer.ExpiresOn)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("the managed identity token's expires_on: %w", err)
	}
	if ok {
		return azcore.AccessToken{Token: answer.AccessToken, ExpiresOn: time.Unix(seconds, 0)}, nil
	}
	seconds, ok, err = jsonSeconds(answer.ExpiresIn)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("the managed identity token's expires_in: %w", err)
	}
	if ok {
		return azcore.AccessToken{Token: answer.AccessToken, ExpiresOn: now.Add(time.Duration(seconds) * time.Second)}, nil
	}
	return azcore.AccessToken{}, errors.New("the managed identity token has neither expires_on nor expires_in")
}

// jsonSeconds reads a whole number of seconds sent as a JSON number or a
// numeric string. ok is false when the field is absent or null.
func jsonSeconds(raw json.RawMessage) (seconds int64, ok bool, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false, nil
	}
	// A json.Number takes a number and a string holding one alike.
	var n json.Number
	if err = json.Unmarshal(raw, &n); err != nil {
		return 0, false, err
	}
	if seconds, err = n.Int64(); err != nil {
		return 0, false, err
	}
	return seconds, true, nil
}

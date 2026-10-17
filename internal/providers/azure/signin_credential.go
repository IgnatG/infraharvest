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
	signInWithOIDC
	signInWithManagedIdentity
)

// String names the method in words, for error messages.
func (m signInMethod) String() string {
	switch m {
	case signInWithCertificate:
		return "a client certificate"
	case signInWithSecret:
		return "a client secret"
	case signInWithOIDC:
		return "OIDC"
	case signInWithManagedIdentity:
		return "a managed identity"
	}
	return "the Azure CLI"
}

// needsApplication says whether the method signs in as an application
// registration, which takes both a tenant and a client ID.
func (m signInMethod) needsApplication() bool {
	return m == signInWithCertificate || m == signInWithSecret || m == signInWithOIDC
}

// method picks how cfg signs in: the first method, in the order azurerm
// documents them, whose settings are all present, and the Azure CLI when
// none is.
func (cfg authConfig) method() signInMethod {
	switch {
	case cfg.certificatePath != "":
		return signInWithCertificate
	case cfg.clientSecret != "":
		return signInWithSecret
	case cfg.useOIDC && cfg.oidcRequestURL != "" && cfg.oidcRequestToken != "":
		return signInWithOIDC
	case cfg.useMSI:
		return signInWithManagedIdentity
	}
	return signInWithCLI
}

// newCredential builds the credential cfg's method signs in with, for cfg's
// cloud. It sends no request: the credential signs in on its first token.
func newCredential(cfg authConfig) (azcore.TokenCredential, error) {
	method := cfg.method()
	if method.needsApplication() && (cfg.tenantID == "" || cfg.clientID == "") {
		return nil, fmt.Errorf("ARM_TENANT_ID and ARM_CLIENT_ID are both required to sign in with %s", method)
	}
	options := azcore.ClientOptions{Cloud: cfg.cloud}
	// A custom cloud's authority is not one Entra's instance discovery
	// knows, so the credentials must take it as it is.
	customAuthority := cfg.metadataHost != ""

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
	case signInWithOIDC:
		client := &http.Client{Timeout: signInHTTPTimeout}
		assertion := func(ctx context.Context) (string, error) {
			return fetchIDToken(ctx, client, cfg.oidcRequestURL, cfg.oidcRequestToken)
		}
		return asCredential(azidentity.NewClientAssertionCredential(cfg.tenantID, cfg.clientID, assertion,
			&azidentity.ClientAssertionCredentialOptions{
				ClientOptions:              options,
				AdditionallyAllowedTenants: cfg.auxiliaryTenants,
				DisableInstanceDiscovery:   customAuthority,
			}))
	case signInWithManagedIdentity:
		if cfg.msiEndpoint != "" {
			return cacheTokens(&msiEndpointCredential{
				endpoint: cfg.msiEndpoint,
				clientID: cfg.clientID,
				client:   &http.Client{Timeout: signInHTTPTimeout},
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

// asCredential returns a constructor's credential as an interface, and a
// nil interface (not a typed nil pointer) when the constructor failed.
func asCredential[C azcore.TokenCredential](credential C, err error) (azcore.TokenCredential, error) {
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// certificateCredential signs in with the certificate (and its private
// key) in the file ARM_CLIENT_CERTIFICATE_PATH names.
func certificateCredential(cfg authConfig, options azcore.ClientOptions, customAuthority bool) (azcore.TokenCredential, error) {
	data, err := os.ReadFile(cfg.certificatePath)
	if err != nil {
		return nil, fmt.Errorf("reading ARM_CLIENT_CERTIFICATE_PATH: %w", err)
	}
	var password []byte
	if cfg.certificatePassword != "" {
		password = []byte(cfg.certificatePassword)
	}
	certificates, key, err := azidentity.ParseCertificates(data, password)
	if err != nil {
		return nil, fmt.Errorf("parsing the certificate in ARM_CLIENT_CERTIFICATE_PATH: %w", err)
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
	query.Set("api-version", msiAPIVersion)
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

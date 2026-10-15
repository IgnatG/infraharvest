// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
)

// maxAuxiliaryTenants is the most auxiliary tenants Resource Manager takes
// in one request's x-ms-authorization-auxiliary header.
const maxAuxiliaryTenants = 3

// maxSignInResponse caps how much of a sign-in related answer (the cloud's
// endpoints, an ID token, a managed identity token) is read, so that a
// misbehaving server can't make the process read without end.
const maxSignInResponse = 1 << 20

// authConfig is what the ARM_* environment says about where and how to sign
// in. loadAuthConfig fills it in; signIn completes the cloud of a custom
// (metadata host) cloud and newCredential turns it into a credential.
type authConfig struct {
	// subscriptionID is the subscription to import; empty when none is set,
	// which Subscriptions accepts and Init does not.
	subscriptionID string

	// tenantID and clientID name the Entra tenant and the application (or
	// user-assigned managed identity) to sign in as.
	tenantID string
	clientID string

	// clientSecret signs in a service principal with a secret.
	clientSecret string

	// certificatePath and certificatePassword sign in a service principal
	// with a PEM or PKCS#12 certificate.
	certificatePath     string
	certificatePassword string

	// useOIDC, oidcRequestURL and oidcRequestToken sign in with a GitHub
	// Actions (or compatible) ID token exchanged for an Entra token.
	useOIDC          bool
	oidcRequestURL   string
	oidcRequestToken string

	// useMSI signs in as a managed identity, through msiEndpoint when it is
	// set and the environment's own identity endpoint otherwise.
	useMSI      bool
	msiEndpoint string

	// auxiliaryTenants are other tenants the credential may get tokens for,
	// for resources that span tenants (such as a peering).
	auxiliaryTenants []string

	// cloud is the Azure cloud to sign in to and call. For a metadata host
	// it stays empty until signIn reads the host's endpoints.
	cloud cloud.Configuration

	// metadataHost is ARM_METADATA_HOSTNAME when ARM_ENVIRONMENT is stack:
	// the host that describes a custom cloud such as Azure Stack Hub.
	metadataHost string
}

// loadAuthConfig reads the ARM_* variables through getenv, which is
// os.Getenv outside tests. It only reads and checks them: it makes no
// network call, so a custom cloud's endpoints are left to signIn.
func loadAuthConfig(getenv func(string) string) (authConfig, error) {
	cfg := authConfig{
		subscriptionID:      getenv("ARM_SUBSCRIPTION_ID"),
		tenantID:            getenv("ARM_TENANT_ID"),
		clientID:            getenv("ARM_CLIENT_ID"),
		clientSecret:        getenv("ARM_CLIENT_SECRET"),
		certificatePath:     getenv("ARM_CLIENT_CERTIFICATE_PATH"),
		certificatePassword: getenv("ARM_CLIENT_CERTIFICATE_PASSWORD"),
		msiEndpoint:         getenv("ARM_MSI_ENDPOINT"),
		oidcRequestURL:      firstSet(getenv, "ARM_OIDC_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_URL"),
		oidcRequestToken:    firstSet(getenv, "ARM_OIDC_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"),
	}

	var err error
	if cfg.useOIDC, err = envBool(getenv, "ARM_USE_OIDC"); err != nil {
		return authConfig{}, err
	}
	if cfg.useMSI, err = envBool(getenv, "ARM_USE_MSI"); err != nil {
		return authConfig{}, err
	}

	for _, tenant := range strings.Split(getenv("ARM_AUXILIARY_TENANT_IDS"), ";") {
		if tenant = strings.TrimSpace(tenant); tenant != "" {
			cfg.auxiliaryTenants = append(cfg.auxiliaryTenants, tenant)
		}
	}
	if len(cfg.auxiliaryTenants) > maxAuxiliaryTenants {
		return authConfig{}, fmt.Errorf("ARM_AUXILIARY_TENANT_IDS names %d tenants, but only %d are supported",
			len(cfg.auxiliaryTenants), maxAuxiliaryTenants)
	}

	environment := strings.ToLower(strings.TrimSpace(getenv("ARM_ENVIRONMENT")))
	if environment == "stack" {
		cfg.metadataHost = strings.TrimSpace(getenv("ARM_METADATA_HOSTNAME"))
		if cfg.metadataHost == "" {
			return authConfig{}, errors.New("ARM_ENVIRONMENT=stack needs ARM_METADATA_HOSTNAME, the host that describes the cloud's endpoints")
		}
		return cfg, nil
	}
	c, err := namedCloud(environment)
	if err != nil {
		return authConfig{}, err
	}
	cfg.cloud = c
	return cfg, nil
}

// envBool reads the boolean variable name the way the azurerm provider
// does, so that a setting means the same to both: whatever
// strconv.ParseBool accepts (1, t, true, 0, f, false and their capitalised
// and upper-case forms), with surrounding spaces ignored. Unset or empty is
// false. Any other value is an error rather than a guess, because reading
// "no" or "yes" wrongly would pick a different sign-in method without a
// word.
func envBool(getenv func(string) string, name string) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return false, nil
	}
	on, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s is %q, which is not a boolean: use true or false", name, value)
	}
	return on, nil
}

// firstSet returns the value of the first of names that is set (not empty).
func firstSet(getenv func(string) string, names ...string) string {
	for _, name := range names {
		if v := getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// namedCloud returns the Azure cloud an ARM_ENVIRONMENT value (already
// trimmed and lower-cased) names. It accepts the names azurerm takes as
// well as the SDK and CLI ones, such as AzurePublicCloud and AzureCloud,
// by dropping a leading "azure" and a trailing "cloud" first.
func namedCloud(environment string) (cloud.Configuration, error) {
	switch strings.TrimSuffix(strings.TrimPrefix(environment, "azure"), "cloud") {
	case "", "public", "global":
		return cloud.AzurePublic, nil
	case "usgovernment", "usgovernmentl4", "usgovernmentl5", "dod":
		return cloud.AzureGovernment, nil
	case "china":
		return cloud.AzureChina, nil
	}
	return cloud.Configuration{}, fmt.Errorf("ARM_ENVIRONMENT %q is not a cloud infraharvest knows: use public, usgovernment, china or stack", environment)
}

// cloudEndpoints is the part of a metadata host's answer that sign-in
// needs. Azure answers with a list of these, one per cloud; Azure Stack Hub
// and emulators answer with one object.
type cloudEndpoints struct {
	ResourceManager string `json:"resourceManager"`
	Authentication  struct {
		LoginEndpoint string   `json:"loginEndpoint"`
		Audiences     []string `json:"audiences"`
	} `json:"authentication"`
}

// cloudFromMetadata reads the endpoints of the custom cloud host describes
// and returns them as a cloud the SDK can sign in to and call.
func cloudFromMetadata(ctx context.Context, client *http.Client, host string) (cloud.Configuration, error) {
	endpointsURL := "https://" + host + "/metadata/endpoints?api-version=2022-09-01"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointsURL, http.NoBody)
	if err != nil {
		return cloud.Configuration{}, fmt.Errorf("reading the endpoints of %s: %w", host, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return cloud.Configuration{}, fmt.Errorf("reading the endpoints of %s: %w", host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSignInResponse))
	if err != nil {
		return cloud.Configuration{}, fmt.Errorf("reading the endpoints of %s: %w", host, err)
	}
	if resp.StatusCode != http.StatusOK {
		return cloud.Configuration{}, fmt.Errorf("reading the endpoints of %s: %s", host, resp.Status)
	}

	endpoints, err := parseCloudEndpoints(body)
	if err != nil {
		return cloud.Configuration{}, fmt.Errorf("the endpoints of %s: %w", host, err)
	}
	auth := endpoints.Authentication
	if auth.LoginEndpoint == "" || len(auth.Audiences) == 0 || auth.Audiences[0] == "" {
		return cloud.Configuration{}, fmt.Errorf("the endpoints of %s have no sign-in data (authentication.loginEndpoint and audiences)", host)
	}
	resourceManager := endpoints.ResourceManager
	if resourceManager == "" {
		// Azure Stack Hub leaves it out: Resource Manager is the host.
		resourceManager = "https://" + host
	}
	return cloud.Configuration{
		ActiveDirectoryAuthorityHost: auth.LoginEndpoint,
		Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			cloud.ResourceManager: {Endpoint: resourceManager, Audience: auth.Audiences[0]},
		},
	}, nil
}

// parseCloudEndpoints decodes a metadata answer: one object, or a list of
// exactly one, since a longer list doesn't say which cloud is meant.
func parseCloudEndpoints(body []byte) (cloudEndpoints, error) {
	var endpoints cloudEndpoints
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		var list []cloudEndpoints
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return cloudEndpoints{}, err
		}
		if len(list) != 1 {
			return cloudEndpoints{}, fmt.Errorf("want one cloud, got a list of %d", len(list))
		}
		return list[0], nil
	}
	if err := json.Unmarshal(body, &endpoints); err != nil {
		return cloudEndpoints{}, err
	}
	return endpoints, nil
}

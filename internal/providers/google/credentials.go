// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
)

// cloudPlatformScope is the scope the listers' credentials are used with.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// The environment variables the Terraform provider takes its credentials
// from, before the application default credentials, which the listers
// take theirs from too, so that both read the project as the same
// principal.
const (
	// accessTokenEnv is an OAuth 2.0 access token, which comes first.
	accessTokenEnv = "GOOGLE_OAUTH_ACCESS_TOKEN"
	// impersonateEnv is a service account to impersonate, with whichever
	// credentials were found.
	impersonateEnv = "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT"
)

// credentialsEnvs name a credentials file, or its JSON contents, in the
// order the Terraform provider reads them, after the access token.
var credentialsEnvs = []string{"GOOGLE_CREDENTIALS", "GOOGLE_CLOUD_KEYFILE_JSON", "GCLOUD_KEYFILE_JSON"}

// credentialOptions are the options of the listers' credentials: those
// the environment variables the Terraform provider reads name, as it
// takes them (an access token, else a credentials file or its contents,
// then a service account to impersonate), or none, for the application
// default credentials. ctx is kept by an impersonated token source, to
// refresh its tokens.
func credentialOptions(ctx context.Context) ([]option.ClientOption, error) {
	opts := []option.ClientOption{option.WithScopes(cloudPlatformScope)}
	if token := os.Getenv(accessTokenEnv); token != "" {
		opts = append(opts, option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})))
	} else if name, value := credentialsEnv(); value != "" {
		credentials, err := credentialsJSON(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		credType, err := credentialsType(credentials)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		opts = append(opts, option.WithAuthCredentialsJSON(credType, credentials))
	}
	target := os.Getenv(impersonateEnv)
	if target == "" {
		return opts, nil
	}
	source, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{TargetPrincipal: target, Scopes: []string{cloudPlatformScope}}, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", impersonateEnv, err)
	}
	return []option.ClientOption{option.WithTokenSource(source)}, nil
}

// credentialsEnv returns the first of credentialsEnvs that is set, and its
// value.
func credentialsEnv() (name, value string) {
	for _, name := range credentialsEnvs {
		if value := os.Getenv(name); value != "" {
			return name, value
		}
	}
	return "", ""
}

// credentialsJSON is the JSON of credentials that value holds, as the
// Terraform provider takes it: the contents themselves, or the path of a
// file that has them.
func credentialsJSON(value string) ([]byte, error) {
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		return []byte(value), nil
	}
	content, err := os.ReadFile(value)
	if err != nil {
		// The value may be the contents, mistyped: don't repeat it.
		return nil, errors.New("neither the JSON of credentials nor a file that can be read")
	}
	return content, nil
}

// credentialsType is the type the credentials say they are, of those the
// Google client libraries load.
func credentialsType(credentials []byte) (option.CredentialsType, error) {
	var file struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(credentials, &file); err != nil {
		return "", errors.New("the credentials aren't JSON")
	}
	switch t := option.CredentialsType(file.Type); t {
	case option.ServiceAccount, option.AuthorizedUser, option.ImpersonatedServiceAccount, option.ExternalAccount:
		return t, nil
	default:
		return "", fmt.Errorf("credentials of type %q, not a service account key, user, impersonated service account or external account (workload identity federation) configuration", file.Type)
	}
}

// failingTokenSource fails each token request with err: the listers' calls
// fail with why the credentials the environment names can't be used,
// rather than read the project as another principal than the Terraform
// provider would.
type failingTokenSource struct {
	err error
}

func (s failingTokenSource) Token() (*oauth2.Token, error) {
	return nil, s.err
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"testing"
)

// Init takes the service account an environment reads its project as
// from args[3], remembers it for the project's clients, and hands it to
// Terraform; a project without one forgets an earlier call's.
func TestInitTakesTheServiceAccount(t *testing.T) {
	t.Setenv(projectEnv, "")
	for _, project := range []string{"shop-dev", "shop-prod"} {
		impersonations.Delete(project)
	}
	const account = "reader@shop-dev.iam.gserviceaccount.com"
	p := &Provider{}
	if err := p.Init(t.Context(), []string{"global", "shop-dev", "", IdentityArg(account)}); err != nil {
		t.Fatal(err)
	}
	if impersonation("shop-dev") != account || impersonation("shop-prod") != "" {
		t.Errorf("impersonations: %q, %q", impersonation("shop-dev"), impersonation("shop-prod"))
	}
	env, err := p.TerraformEnv(t.Context())
	if err != nil || env[impersonateEnv] != account || len(env) != 1 {
		t.Errorf("Terraform's environment: %v, %v", env, err)
	}
	other := &Provider{}
	if err := other.Init(t.Context(), []string{"global", "shop-prod", ""}); err != nil {
		t.Fatal(err)
	}
	if env, _ := other.TerraformEnv(t.Context()); len(env) != 0 {
		t.Errorf("a project read as the environment: %v", env)
	}
	if err := other.Init(t.Context(), []string{"global", "shop-dev", ""}); err != nil || impersonation("shop-dev") != "" {
		t.Errorf("forgotten: %q, %v", impersonation("shop-dev"), err)
	}
}

// Each service account impersonated has credentials, and a client, of its
// own; the project without one shares the environment's.
func TestCredentialsPerServiceAccount(t *testing.T) {
	clearCredentialsEnv(t)
	t.Setenv(accessTokenEnv, "token")
	credentials.Clear()
	t.Cleanup(credentials.Clear)
	plain := credentialsClient(t.Context(), "")
	a := credentialsClient(t.Context(), "a@p.iam.gserviceaccount.com")
	b := credentialsClient(t.Context(), "b@p.iam.gserviceaccount.com")
	if plain == nil || a == nil || b == nil || plain == a || a == b {
		t.Fatalf("clients: %p, %p, %p", plain, a, b)
	}
	if again := credentialsClient(t.Context(), "a@p.iam.gserviceaccount.com"); again != a {
		t.Error("a service account's credentials are found once")
	}
}

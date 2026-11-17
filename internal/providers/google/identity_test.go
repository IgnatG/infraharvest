// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"testing"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
	"github.com/IgnatG/infraharvest/internal/runstate"
)

// Init takes the service account an environment reads its project as
// from the settings, remembers it for the project's clients, and hands it to
// Terraform; a project without one forgets an earlier call's.
func TestInitTakesTheServiceAccount(t *testing.T) {
	t.Setenv(projectEnv, "")
	ctx := testRun(t)
	const account = "reader@shop-dev.iam.gserviceaccount.com"
	p := &Provider{}
	if err := p.Init(ctx, Settings{Region: "global", Project: "shop-dev", ServiceAccount: account}); err != nil {
		t.Fatal(err)
	}
	if impersonation(ctx, "shop-dev") != account || impersonation(ctx, "shop-prod") != "" {
		t.Errorf("impersonations: %q, %q", impersonation(ctx, "shop-dev"), impersonation(ctx, "shop-prod"))
	}
	env, err := p.TerraformEnv(ctx)
	if err != nil || env[impersonateEnv] != account || len(env) != 1 {
		t.Errorf("Terraform's environment: %v, %v", env, err)
	}
	other := &Provider{}
	if err := other.Init(ctx, Settings{Region: "global", Project: "shop-prod"}); err != nil {
		t.Fatal(err)
	}
	if env, _ := other.TerraformEnv(ctx); len(env) != 0 {
		t.Errorf("a project read as the environment: %v", env)
	}
	if err := other.Init(ctx, Settings{Region: "global", Project: "shop-dev"}); err != nil || impersonation(ctx, "shop-dev") != "" {
		t.Errorf("forgotten: %q, %v", impersonation(ctx, "shop-dev"), err)
	}
}

// Each service account impersonated has credentials, and a client, of its
// own; the project without one shares the environment's. Another run
// finds them again.
func TestCredentialsPerServiceAccount(t *testing.T) {
	clearCredentialsEnv(t)
	t.Setenv(accessTokenEnv, "token")
	ctx := runstate.NewContext(t.Context(), runstate.New(ratelimit.New(0)))
	plain := credentialsClient(ctx, "")
	a := credentialsClient(ctx, "a@p.iam.gserviceaccount.com")
	b := credentialsClient(ctx, "b@p.iam.gserviceaccount.com")
	if plain == nil || a == nil || b == nil || plain == a || a == b {
		t.Fatalf("clients: %p, %p, %p", plain, a, b)
	}
	if again := credentialsClient(ctx, "a@p.iam.gserviceaccount.com"); again != a {
		t.Error("a service account's credentials are found once")
	}
	other := runstate.NewContext(t.Context(), runstate.New(ratelimit.New(0)))
	if credentialsClient(other, "a@p.iam.gserviceaccount.com") == a {
		t.Error("another run got the first run's credentials")
	}
}

// Two runs, such as a server's, that read the same project as different
// service accounts each keep their own, and the credentials each takes.
func TestRunsKeepTheirOwnServiceAccount(t *testing.T) {
	t.Setenv(projectEnv, "")
	accounts := []string{"first@shop.iam.gserviceaccount.com", "second@shop.iam.gserviceaccount.com"}
	runs := make([]context.Context, len(accounts))
	for i := range runs {
		runs[i] = runstate.NewContext(t.Context(), runstate.New(ratelimit.Like(ratelimit.Default())))
	}
	for i, account := range accounts {
		t.Run(account, func(t *testing.T) {
			t.Parallel()
			p := &Provider{}
			if err := p.Init(runs[i], Settings{Region: "global", Project: "shop", ServiceAccount: account}); err != nil {
				t.Fatal(err)
			}
			if got := impersonation(runs[i], "shop"); got != account {
				t.Errorf("run %d reads shop as %q, want %q", i, got, account)
			}
		})
	}
	t.Cleanup(func() {
		for i, account := range accounts {
			if got := impersonation(runs[i], "shop"); got != account {
				t.Errorf("run %d reads shop as %q after both, want %q", i, got, account)
			}
		}
	})
}

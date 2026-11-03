// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal

package providers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

const identitySample = `version: 1
environments:
  dev:
    aws: { accounts: ["111111111111"], profile: dev-sso, assume_role: infraharvest-readonly }
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000001"], tenant_id: 11111111-1111-1111-1111-111111111111 }
    google: { projects: [shop-dev], impersonate_service_account: reader@shop-dev.iam.gserviceaccount.com }
  prod:
    aws: { accounts: ["222222222222"], assume_role: "arn:aws:iam::{account}:role/audit/readonly" }
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000002"], client_id: 22222222-2222-2222-2222-222222222222 }
    google: { projects: [shop-prod] }
`

// identityFor returns the identity the file's environments say scope of
// the command is read with.
func identityFor(t *testing.T, f *config.File, command, scope string) config.Identity {
	t.Helper()
	for _, p := range Profiles() {
		if p.Command == command {
			return f.Environments.IdentityFor(p, scope)
		}
	}
	t.Fatalf("no %s command", command)
	return nil
}

func loadIdentity(t *testing.T) *config.File {
	t.Helper()
	file := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(file, []byte(identitySample), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(file, Profiles())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Each environment's scopes are read with the identity of its own: the
// call's args are the command's own for a scope no environment gives one,
// and the environment's for one it does.
func TestIdentityArgsPerEnvironment(t *testing.T) {
	f := loadIdentity(t)
	for _, tc := range []struct {
		name, command, scope string
		args                 []string
		apply                func([]string, string, config.Identity) []string
		want                 []string
	}{
		{"aws dev: profile and role name", "aws", "111111111111", []string{"eu-west-2", "base", "arn:aws:iam::111111111111:role/infraharvest-readonly"}, awsIdentityArgs,
			[]string{"eu-west-2", "dev-sso", "arn:aws:iam::111111111111:role/infraharvest-readonly"}},
		{"aws prod: ARN with {account}, the run's profile", "aws", "222222222222", []string{"eu-west-2", "base", "x"}, awsIdentityArgs,
			[]string{"eu-west-2", "base", "arn:aws:iam::222222222222:role/audit/readonly"}},
		{"azure dev: tenant", "azure", "00000000-0000-0000-0000-000000000001", []string{"rg", "00000000-0000-0000-0000-000000000001"}, azureIdentityArgs,
			[]string{"rg", "00000000-0000-0000-0000-000000000001", "", "tenant_id=11111111-1111-1111-1111-111111111111"}},
		{"azure prod: client, with azapi kept", "azure", "00000000-0000-0000-0000-000000000002", []string{"rg", "00000000-0000-0000-0000-000000000002", "azapi"}, azureIdentityArgs,
			[]string{"rg", "00000000-0000-0000-0000-000000000002", "azapi", "client_id=22222222-2222-2222-2222-222222222222"}},
		{"google dev: service account", "google", "shop-dev", []string{"global", "shop-dev", ""}, googleIdentityArgs,
			[]string{"global", "shop-dev", "", "impersonate_service_account=reader@shop-dev.iam.gserviceaccount.com"}},
	} {
		id := identityFor(t, f, tc.command, tc.scope)
		if len(id) == 0 {
			t.Errorf("%s: no identity", tc.name)
			continue
		}
		if got := tc.apply(slices.Clone(tc.args), tc.scope, id); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// Prod's Google project names none, and a scope no environment names
	// has none: both keep the command's own.
	for _, scope := range []struct{ command, scope string }{{"google", "shop-prod"}, {"google", "other"}, {"aws", "999999999999"}} {
		if id := identityFor(t, f, scope.command, scope.scope); len(id) > 0 {
			t.Errorf("%s %s: got %v, want none", scope.command, scope.scope, id)
		}
	}
}

// A plan's calls take the identity of the scope they cover, and no
// other's.
func TestPlanAppliesIdentityPerScope(t *testing.T) {
	f := loadIdentity(t)
	accounts := []string{"111111111111", "222222222222", "333333333333"}
	p, err := (&awsScope{accounts: accounts, profile: "base", assumeRole: DefaultAssumeRole}).plan(t.Context(), importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	profile := (&awsProfileOnly{}).profile()
	p.identify = func(args []string, scope string) []string {
		if id := f.Environments.IdentityFor(profile, scope); len(id) > 0 {
			return awsIdentityArgs(slices.Clone(args), scope, id)
		}
		return args
	}
	plan := p.importerPlan(importer.Options{}, newAWSProvider)
	got := map[string][]string{}
	for _, account := range plan.Scopes {
		for _, c := range plan.Calls(account) {
			got[account] = c.Args[1:]
		}
	}
	want := map[string][]string{
		"111111111111": {"dev-sso", "arn:aws:iam::111111111111:role/infraharvest-readonly"},
		"222222222222": {"base", "arn:aws:iam::222222222222:role/audit/readonly"},
		"333333333333": {"base", "arn:aws:iam::333333333333:role/infraharvest-readonly"},
	}
	for account, w := range want {
		if !slices.Equal(got[account], w) {
			t.Errorf("%s: got %q, want %q", account, got[account], w)
		}
	}
}

// awsProfileOnly finds the AWS profile of the binary.
type awsProfileOnly struct{}

func (awsProfileOnly) profile() harvest.Profile {
	return newAWSProvider().Profile()
}

// The file holds references: a key the command doesn't have, a secret and
// a role in another account are errors.
func TestIdentityIsValidated(t *testing.T) {
	for name, tc := range map[string]struct{ section, want string }{
		"unknown key":      {`aws: { accounts: ["111111111111"], secret_key: x }`, "unknown key"},
		"azure key on aws": {`aws: { accounts: ["111111111111"], tenant_id: 11111111-1111-1111-1111-111111111111 }`, "unknown key"},
		"access key":       {`aws: { accounts: ["111111111111"], profile: AKIAIOSFODNN7EXAMPLE }`, "looks like a secret"},
		"secret key":       {`aws: { accounts: ["111111111111"], assume_role: wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY12 }`, "looks like a secret"},
		"azure secret":     {`azure: { subscriptions: [s], client_id: "abc8Q~Xy1234567890abcdefghijklmnopqrstuv" }`, "looks like a secret"},
		"not a GUID":       {`azure: { subscriptions: [s], client_id: my-app }`, "isn't a client ID"},
		"key file":         {`google: { projects: [p], impersonate_service_account: '{"type": "service_account"}' }`, "looks like a secret"},
		"not an email":     {`google: { projects: [p], impersonate_service_account: reader }`, "isn't a service account"},
		"role elsewhere":   {`aws: { accounts: ["111111111111"], assume_role: "arn:aws:iam::222222222222:role/x" }`, "is in account 222222222222"},
		"no scopes":        {`aws: { profile: dev }`, "name its accounts"},
		"a list":           {`aws: { accounts: ["111111111111"], profile: [a, b] }`, "takes one value"},
		"accounts as one":  {`aws: { accounts: "111111111111" }`, "is a list of IDs"},
		"empty":            {`aws: { accounts: ["111111111111"], profile: "" }`, "is empty"},
	} {
		file := filepath.Join(t.TempDir(), "infraharvest.yaml")
		if err := os.WriteFile(file, []byte("version: 1\nenvironments:\n  dev:\n    "+tc.section+"\n    tags: { Environment: dev }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(file, Profiles()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

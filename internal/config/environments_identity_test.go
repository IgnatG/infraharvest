// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// identityProfiles are testProfiles with an identity for each command.
func identityProfiles() []harvest.Profile {
	profiles := make([]harvest.Profile, len(testProfiles))
	copy(profiles, testProfiles)
	profiles[0].Scope.Identity = []harvest.IdentityKey{
		{Key: "profile", Match: regexp.MustCompile(`^[a-z-]+$`), Format: "a profile name"},
		{Key: "assume_role"},
	}
	profiles[1].Scope.Identity = []harvest.IdentityKey{{Key: "tenant_id"}}
	return profiles
}

func TestLoadEnvironmentIdentity(t *testing.T) {
	t.Parallel()
	f, err := Load(write(t, `version: 1
environments:
  dev:
    aws: { accounts: ["111111111111"], profile: dev-sso, assume_role: reader }
    azure: { subscriptions: [S1], tenant_id: contoso.example }
    tags: { Environment: dev }
  prod:
    aws: { accounts: ["222222222222"] }
`), identityProfiles())
	if err != nil {
		t.Fatal(err)
	}
	aws := identityProfiles()[0]
	if got := f.Environments.IdentityFor(aws, "111111111111"); !reflect.DeepEqual(got, Identity{"profile": "dev-sso", "assume_role": "reader"}) {
		t.Errorf("dev: %v", got)
	}
	if got := f.Environments.IdentityFor(aws, "222222222222"); len(got) != 0 {
		t.Errorf("prod names none: %v", got)
	}
	if got := f.Environments.IdentityFor(identityProfiles()[1], "s1"); got["tenant_id"] != "contoso.example" {
		t.Errorf("azure scopes match in any case: %v", got)
	}
	// The scopes are as without an identity.
	if got := f.Environments["dev"].Scopes("aws"); !reflect.DeepEqual(got, []string{"111111111111"}) {
		t.Errorf("scopes: %v", got)
	}
}

func TestEnvironmentIdentityErrors(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ file, want string }{
		"unknown key on the environment":    {"environments:\n  dev:\n    aws: { accounts: [\"111111111111\"] }\n    profile: x\n", "a mapping"},
		"duplicate key":                     {"environments:\n  dev:\n    aws: { accounts: [\"111111111111\"], profile: a, profile: b }\n", "set twice"},
		"profile format":                    {"environments:\n  dev:\n    aws: { accounts: [\"111111111111\"], profile: Dev_1 }\n", "isn't a profile name"},
		"tenant on aws":                     {"environments:\n  dev:\n    aws: { accounts: [\"111111111111\"], tenant_id: x }\n", "unknown key"},
		"different identities, one account": {"environments:\n  dev:\n    aws: { accounts: [\"111111111111\"], profile: a }\n    tags: { E: dev }\n  prod:\n    aws: { accounts: [\"111111111111\"], profile: b }\n    tags: { E: prod }\n", "different identities"},
		"identity on a scope-less section":  {"environments:\n  dev:\n    aws: { profile: a }\n    tags: { E: dev }\n", "name its accounts"},
	} {
		_, err := Load(write(t, "version: 1\n"+tc.file), identityProfiles())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	// Environments that share an account and agree on its identity are
	// read the same.
	_, err := Load(write(t, "version: 1\nenvironments:\n  dev:\n    aws: { accounts: [\"111111111111\"], profile: a }\n    tags: { E: dev }\n  prod:\n    aws: { accounts: [\"111111111111\"], profile: a }\n    tags: { E: prod }\n"), identityProfiles())
	if err != nil {
		t.Errorf("agreeing environments: %v", err)
	}
}

func TestLooksSecret(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"AKIAIOSFODNN7EXAMPLE":                     true,
		"wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY12": true,
		"eyJhbGciOiJIUzI1NiJ9.e30.abc":             true,
		"ya29.a0AfH6SMB":                           true,
		"ghp_abcdef":                               true,
		"-----BEGIN PRIVATE KEY-----":              true,
		"abc8Q~Xy1234567890abcdefghijklmnop":       true,
		"dev-sso":                                  false,
		"arn:aws:iam::111111111111:role/readonly":  false,
		"reader@shop.iam.gserviceaccount.com":      false,
		"11111111-1111-1111-1111-111111111111":     false,
	} {
		if got := looksSecret(value); got != want {
			t.Errorf("looksSecret(%q) = %v", value, got)
		}
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// testProfiles are profiles like those of the provider commands, as far
// as the configuration file reads them.
var testProfiles = []harvest.Profile{
	{Command: "aws", Scope: harvest.Scope{Noun: "account", Field: "accounts", ID: regexp.MustCompile(`^[0-9]{12}$`), IDFormat: "an account ID, 12 digits"}},
	{Command: "azure", Scope: harvest.Scope{Noun: "subscription", Field: "subscriptions", FoldCase: true}},
	{Command: "google", Scope: harvest.Scope{Noun: "project", Field: "projects"}},
}

// testProfile returns the profile of testProfiles with the command.
func testProfile(command string) harvest.Profile {
	i := slices.IndexFunc(testProfiles, func(p harvest.Profile) bool { return p.Command == command })
	return testProfiles[i]
}

const environmentsSample = `version: 1
environments:
  dev:
    aws: { accounts: ["111111111111"] }
    azure: { subscriptions: ["0B1C2D3E-0000-0000-0000-000000000001"] }
    tokens: [dev, development]
  staging:
    aws: { accounts: ["222222222222"] }
    tags: { Environment: staging }
  prod:
    aws: { accounts: ["222222222222", "333333333333"] }
    google: { projects: [shop-prod] }
    tags: { Environment: Prod }
    tokens: [prd]
  sandbox:
    tags: { Environment: sandbox }
  demo:
    tags: { Environment: demo }
`

func TestLoadEnvironments(t *testing.T) {
	t.Parallel()
	f, err := Load(write(t, environmentsSample), testProfiles)
	if err != nil {
		t.Fatal(err)
	}
	envs := f.Environments
	for _, tc := range []struct {
		provider, scope string
		want            []string
	}{
		{"aws", "111111111111", []string{"dev"}},
		// An account two environments share, which their tags split.
		{"aws", "222222222222", []string{"prod", "staging"}},
		{"aws", "333333333333", []string{"prod"}},
		// Subscription IDs in either case.
		{"azure", "0b1c2d3e-0000-0000-0000-000000000001", []string{"dev"}},
		{"google", "shop-prod", []string{"prod"}},
		// What no environment names is told by the tags of those that
		// name nothing.
		{"aws", "444444444444", []string{"demo", "sandbox"}},
		{"google", "shop-dev", []string{"demo", "sandbox"}},
		// An account is never a project.
		{"google", "111111111111", []string{"demo", "sandbox"}},
	} {
		if got := envs.For(testProfile(tc.provider), tc.scope); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("For(%s, %s) = %v, want %v", tc.provider, tc.scope, got, tc.want)
		}
	}
	if got := envs.EnvironmentTokens("prod"); !reflect.DeepEqual(got, []string{"prd"}) {
		t.Errorf("prod's tokens: %v", got)
	}
	if got := envs.EnvironmentTokens("staging"); !reflect.DeepEqual(got, []string{"staging"}) {
		t.Errorf("staging's tokens default to its name: %v", got)
	}
}

// Without environments that name nothing, a scope no environment names
// has none: its roots are laid out as without environments.
func TestEnvironmentsForUnnamedScope(t *testing.T) {
	t.Parallel()
	envs := Environments{"dev": {AWS: &AWSScopes{Accounts: []string{"111111111111"}}}}
	if got := envs.For(testProfile("aws"), "999999999999"); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
	if got := Environments(nil).For(testProfile("aws"), "111111111111"); len(got) != 0 {
		t.Errorf("no environments: got %v", got)
	}
}

func TestEnvironmentMatches(t *testing.T) {
	t.Parallel()
	prod := Environment{Tags: map[string]string{"Environment": "Prod", "Team": "shop"}}
	for _, tc := range []struct {
		tags map[string]string
		want bool
	}{
		{map[string]string{"Environment": "prod", "Team": "Shop", "Name": "web"}, true},
		{map[string]string{"Environment": " PROD ", "Team": "shop"}, true},
		{map[string]string{"Environment": "prod"}, false},
		{map[string]string{"environment": "prod", "Team": "shop"}, false},
		{nil, false},
	} {
		if got := prod.Matches(tc.tags); got != tc.want {
			t.Errorf("Matches(%v) = %v, want %v", tc.tags, got, tc.want)
		}
	}
	if (Environment{}).Matches(map[string]string{"Environment": "prod"}) {
		t.Error("an environment without tags matched")
	}
}

func TestLoadRejectsEnvironmentMistakes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ environments, want string }{
		"a name that isn't a directory's": {
			"  Prod EU: { aws: { accounts: [\"111111111111\"] } }\n", "an environment's name",
		},
		"the shared name": {
			"  shared: { aws: { accounts: [\"111111111111\"] } }\n", "taken",
		},
		"the all name": {
			"  all: { aws: { accounts: [\"111111111111\"] } }\n", "every environment",
		},
		"nothing to find it by": {
			"  dev: { tokens: [dev] }\n", "name its aws accounts",
		},
		"not an account": {
			"  dev: { aws: { accounts: [\"1234\"] } }\n", "isn't an account ID",
		},
		"an empty project": {
			"  dev: { google: { projects: [\"\"] } }\n", "an empty ID",
		},
		"a tag without a value": {
			"  dev: { tags: { Environment: \"\" } }\n", "needs a key and a value",
		},
		"a token two environments use": {
			"  dev: { aws: { accounts: [\"111111111111\"] }, tokens: [dev] }\n  test: { aws: { accounts: [\"222222222222\"] }, tokens: [Dev] }\n", "is dev's too",
		},
		"an account in two without tags": {
			"  dev: { aws: { accounts: [\"111111111111\"] } }\n  prod: { aws: { accounts: [\"111111111111\"] }, tags: { Environment: prod } }\n", "dev has no tags",
		},
		"an account in two with tags that don't tell them apart": {
			"  dev: { aws: { accounts: [\"111111111111\"] }, tags: { Environment: dev } }\n  prod: { aws: { accounts: [\"111111111111\"] }, tags: { Team: shop } }\n", "don't tell their resources apart",
		},
		"a subscription in two in another case": {
			"  dev: { azure: { subscriptions: [\"ABC\"] } }\n  prod: { azure: { subscriptions: [\"abc\"] } }\n", "has no tags",
		},
		"tags only, the same": {
			"  dev: { tags: { Environment: Dev } }\n  test: { tags: { Environment: dev } }\n", "don't tell their resources apart",
		},
		"an unknown key": {
			"  dev: { aws: { account: [\"111111111111\"] } }\n", "account",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, "version: 1\nenvironments:\n"+tc.environments), testProfiles)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// What the file reads of a provider command comes from its profile: a
// fourth provider's accounts, IDs and settings take no change to the
// configuration, but for the section that names its scopes in an
// environment, which the file's format has for aws, azure and google
// alone.
func TestEnvironmentsOfAnotherProvider(t *testing.T) {
	t.Parallel()
	oracle := harvest.Profile{Command: "oracle", Scope: harvest.Scope{Noun: "tenancy", Field: "tenancies"}}
	profiles := append(slices.Clone(testProfiles), oracle)
	_, err := Load(write(t, "version: 1\nenvironments:\n  dev: { tokens: [dev] }\n"), profiles)
	if want := "name its aws accounts, azure subscriptions, google projects or oracle tenancies, or its tags"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("an environment that names nothing: got %v, want %q", err, want)
	}
	_, err = Load(write(t, "version: 1\nenvironments:\n  dev: { tags: { Environment: dev } }\n  test: { tags: { Environment: Dev } }\n"), profiles)
	if want := "the environments that name no account, subscription, project or tenancy"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("environments told apart by nothing: got %v, want %q", err, want)
	}
	// The format's sections are its own.
	if _, err := Load(write(t, "version: 1\nenvironments:\n  dev: { oracle: { tenancies: [ocid1.tenancy.oc1..a] } }\n"), profiles); err == nil || !strings.Contains(err.Error(), "oracle") {
		t.Errorf("an oracle section: got %v, want it unknown to the format", err)
	}
	// The other providers' sections hold any ID where the binary has no
	// profile of theirs, as it never reads them.
	if _, err := Load(write(t, "version: 1\nenvironments:\n  dev: { aws: { accounts: [\"1234\"] } }\n"), []harvest.Profile{oracle}); err != nil {
		t.Errorf("an aws account without aws's profile: %v", err)
	}
	f, err := Load(write(t, "version: 1\nproviders:\n  oracle:\n    region: uk-london-1\n"), profiles)
	if err != nil {
		t.Fatal(err)
	}
	flags := pflag.NewFlagSet("oracle", pflag.ContinueOnError)
	region := flags.String("region", "", "")
	if err := f.Apply(flags, "oracle"); err != nil || *region != "uk-london-1" {
		t.Errorf("oracle's settings: got region %q, %v", *region, err)
	}
	if got := (Environments{"dev": {Tags: map[string]string{"Environment": "dev"}}}).For(oracle, "ocid1.tenancy.oc1..a"); !slices.Equal(got, []string{"dev"}) {
		t.Errorf("an oracle tenancy: got %v, want the environment told by tags", got)
	}
}

func TestCheckEnvironmentFlags(t *testing.T) {
	t.Parallel()
	const defaultLayout = "{out}/{provider}/{account}/{region}/"
	with := &File{Environments: Environments{"dev": {Tags: map[string]string{"Environment": "dev"}}}}
	for _, tc := range []struct {
		file    *File
		layout  string
		envTags []string
		want    string
	}{
		{with, defaultLayout, nil, ""},
		{with, "", nil, ""},
		{with, "{out}/{env}/{provider}/{account}/", nil, ""},
		{with, "{out}/{provider}/{service}/", nil, "has no {env}"},
		{with, defaultLayout, []string{"Environment"}, "use one of them"},
		// Without environments, the flags are --env-tags' to check.
		{&File{}, "{out}/{provider}/{service}/", []string{"Environment"}, ""},
	} {
		err := tc.file.CheckEnvironmentFlags(tc.layout, tc.envTags, defaultLayout)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q %v: %v", tc.layout, tc.envTags, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%q %v: got %v, want %q", tc.layout, tc.envTags, err, tc.want)
		}
	}
}

func TestCheckMergeEnvironments(t *testing.T) {
	t.Parallel()
	envs := Environments{"dev": {AWS: &AWSScopes{Accounts: []string{"111111111111"}}}}
	for _, tc := range []struct {
		envs       Environments
		merge, set bool
		fails      bool
	}{
		{nil, true, false, false},  // the default, with nothing to merge
		{nil, false, true, false},  // turned off
		{nil, true, true, true},    // asked for, with nothing to merge
		{envs, true, true, false},  // asked for, with environments
		{envs, false, true, false}, // turned off, with environments
	} {
		if err := CheckMergeEnvironments(tc.envs, tc.merge, tc.set); (err != nil) != tc.fails {
			t.Errorf("CheckMergeEnvironments(%v, %v, %v) = %v", tc.envs, tc.merge, tc.set, err)
		}
	}
}

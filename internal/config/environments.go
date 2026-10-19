// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// SharedEnvironment is the environment of what no environment's tags
// place in an account, subscription or project several environments
// share, such as a network they all use. No environment of the file may
// take its name.
const SharedEnvironment = "shared"

// Environments are the estate's environments, by name, such as dev and
// prod: the name is the directory of the environment's roots.
type Environments map[string]Environment

// Environment is where one environment is: the AWS accounts, Azure
// subscriptions and Google Cloud projects that hold it and, where one of
// them holds several environments, or for an environment that names
// none, the tags that tell its resources apart.
type Environment struct {
	AWS    *AWSScopes    `yaml:"aws,omitempty"`
	Azure  *AzureScopes  `yaml:"azure,omitempty"`
	Google *GoogleScopes `yaml:"google,omitempty"`
	// Tags are tag keys and values (labels on Google Cloud) a resource of
	// the environment has, all of them, compared as --env-tags compares
	// environments: in lower case, with anything but letters, digits,
	// dots, dashes and underscores made a dash.
	Tags map[string]string `yaml:"tags,omitempty"`
	// Tokens are the words that stand for the environment inside
	// resources' names, such as prd for prod; by default its name.
	Tokens []string `yaml:"tokens,omitempty"`
}

// AWSScopes are the AWS accounts of an environment, by ID.
type AWSScopes struct {
	Accounts []string `yaml:"accounts"`
}

// AzureScopes are the Azure subscriptions of an environment, by ID.
type AzureScopes struct {
	Subscriptions []string `yaml:"subscriptions"`
}

// GoogleScopes are the Google Cloud projects of an environment, by ID.
type GoogleScopes struct {
	Projects []string `yaml:"projects"`
}

// environmentName matches an environment's name, which names a directory.
var environmentName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// awsAccount matches an AWS account ID.
var awsAccount = regexp.MustCompile(`^[0-9]{12}$`)

// Scopes returns the accounts, subscriptions or projects of e for the
// provider command provider: aws, azure or google.
func (e Environment) Scopes(provider string) []string {
	switch {
	case provider == "aws" && e.AWS != nil:
		return e.AWS.Accounts
	case provider == "azure" && e.Azure != nil:
		return e.Azure.Subscriptions
	case provider == "google" && e.Google != nil:
		return e.Google.Projects
	}
	return nil
}

// hasScopes reports whether e names any account, subscription or project.
func (e Environment) hasScopes() bool {
	return slices.ContainsFunc(ProviderCommands, func(p string) bool { return len(e.Scopes(p)) > 0 })
}

// EnvironmentTokens returns the words that stand for the environment name
// inside resources' names: its tokens, or else its name.
func (envs Environments) EnvironmentTokens(name string) []string {
	if tokens := envs[name].Tokens; len(tokens) > 0 {
		return tokens
	}
	return []string{name}
}

// Matches reports whether a resource with tags is one of e's: it has
// every tag of e, with the same value as --env-tags compares them. An
// environment without tags matches none.
func (e Environment) Matches(tags map[string]string) bool {
	if len(e.Tags) == 0 {
		return false
	}
	for key, value := range e.Tags {
		have, ok := tags[key]
		if !ok || TagValue(have) != TagValue(value) {
			return false
		}
	}
	return true
}

// tagValueChars are the characters a tag value keeps when environments
// are compared by it.
var tagValueChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// TagValue is a tag value as an environment's name: lower case, with
// anything but letters, digits, dots, dashes and underscores made a
// dash, and no dash or dot at either end; "" if nothing is left.
func TagValue(value string) string {
	return strings.Trim(tagValueChars.ReplaceAllString(strings.ToLower(value), "-"), "-.")
}

// For returns, sorted, the environments of the account, subscription or
// project scope of the provider command provider: those that name it;
// if none does, those that name no account, subscription or project and
// tell their resources by tags only. None means the scope's roots are
// laid out as without environments.
func (envs Environments) For(provider, scope string) []string {
	var named, tagged []string
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		e := envs[name]
		switch {
		case slices.ContainsFunc(e.Scopes(provider), func(s string) bool { return sameScope(provider, s, scope) }):
			named = append(named, name)
		case !e.hasScopes():
			tagged = append(tagged, name)
		}
	}
	if len(named) > 0 {
		return named
	}
	return tagged
}

// sameScope reports whether a and b name the same scope of provider:
// Azure's subscription IDs are GUIDs, in either case.
func sameScope(provider, a, b string) bool {
	if provider == "azure" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// check returns why envs can't place every resource in one environment,
// if they can't: a name that isn't a directory name or is
// SharedEnvironment, an environment that names nothing to find it by, an
// empty account, subscription, project, tag or token, an AWS account that
// isn't one, a token two environments use, or an account, subscription
// or project several environments name, or several environments that
// name none, whose tags don't tell them apart (see apart).
func (envs Environments) check() error {
	tokens := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		e := envs[name]
		switch {
		case !environmentName.MatchString(name):
			return fmt.Errorf("%q: an environment's name is a directory's: lower-case letters, digits, dots, dashes and underscores", name)
		case name == SharedEnvironment:
			return fmt.Errorf("%q: the name is taken by what no environment's tags place", name)
		case !e.hasScopes() && len(e.Tags) == 0:
			return fmt.Errorf("%s: name its aws accounts, azure subscriptions or google projects, or its tags", name)
		}
		for _, provider := range ProviderCommands {
			for _, scope := range e.Scopes(provider) {
				switch {
				case strings.TrimSpace(scope) == "":
					return fmt.Errorf("%s: %s: an empty ID", name, provider)
				case provider == "aws" && !awsAccount.MatchString(scope):
					return fmt.Errorf("%s: aws: %q isn't an account ID, 12 digits", name, scope)
				}
			}
		}
		for key, value := range e.Tags {
			if strings.TrimSpace(key) == "" || TagValue(value) == "" {
				return fmt.Errorf("%s: tags: %q: a tag needs a key and a value", name, key)
			}
		}
		for _, token := range e.Tokens {
			lower := strings.ToLower(strings.TrimSpace(token))
			if lower == "" || strings.ContainsAny(lower, " \t/") {
				return fmt.Errorf("%s: tokens: %q isn't a word", name, token)
			}
			if other, taken := tokens[lower]; taken && other != name {
				return fmt.Errorf("%s: tokens: %q is %s's too", name, token, other)
			}
			tokens[lower] = name
		}
	}
	// What several environments share must be told apart by tags.
	for _, provider := range ProviderCommands {
		sharing := map[string][]string{}
		var scopes []string
		for _, name := range slices.Sorted(maps.Keys(envs)) {
			for _, scope := range envs[name].Scopes(provider) {
				key := scope
				if provider == "azure" {
					key = strings.ToLower(scope)
				}
				if !slices.Contains(sharing[key], name) {
					if len(sharing[key]) == 0 {
						scopes = append(scopes, key)
					}
					sharing[key] = append(sharing[key], name)
				}
			}
		}
		for _, scope := range scopes {
			if err := envs.apart(sharing[scope]); err != nil {
				return fmt.Errorf("%s %s: %w", provider, scope, err)
			}
		}
	}
	var tagsOnly []string
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		if !envs[name].hasScopes() {
			tagsOnly = append(tagsOnly, name)
		}
	}
	if err := envs.apart(tagsOnly); err != nil {
		return fmt.Errorf("the environments that name no account, subscription or project: %w", err)
	}
	return nil
}

// apart returns why the environments names, found in the same place,
// can't be told apart, if they can't: one has no tags, or two have no
// tag key in common with different values, so a resource could be in
// both.
func (envs Environments) apart(names []string) error {
	if len(names) < 2 {
		return nil
	}
	for i, a := range names {
		if len(envs[a].Tags) == 0 {
			return fmt.Errorf("%s are in it, and %s has no tags to tell its resources apart", strings.Join(names, ", "), a)
		}
		for _, b := range names[i+1:] {
			if !tagsApart(envs[a].Tags, envs[b].Tags) {
				return fmt.Errorf("the tags of %s and %s don't tell their resources apart: give them a tag key with different values", a, b)
			}
		}
	}
	return nil
}

// tagsApart reports whether no resource can have both a and b: they have
// a key in common with different values.
func tagsApart(a, b map[string]string) bool {
	for key, value := range a {
		if other, ok := b[key]; ok && TagValue(other) != TagValue(value) {
			return true
		}
	}
	return false
}

// errEnvironmentsAndEnvTags is the error for a file with environments
// used with --env-tags, which would place resources in environments of
// its own.
var errEnvironmentsAndEnvTags = errors.New("environments: and --env-tags both say what is in each environment: use one of them")

// CheckEnvironmentFlags rejects flags the file's environments can't go
// with: --env-tags, which places resources in environments by itself,
// and a --layout of the user's own without {env}, where the environments'
// roots would go. defaultLayout is --layout's default.
func (f *File) CheckEnvironmentFlags(layout string, envTags []string, defaultLayout string) error {
	if len(f.Environments) == 0 {
		return nil
	}
	if len(envTags) > 0 {
		return errEnvironmentsAndEnvTags
	}
	if layout != "" && layout != defaultLayout && !strings.Contains(layout, "{env}") {
		return fmt.Errorf("environments: splits roots by environment, but --layout %q has no {env}: add it where the environment goes", layout)
	}
	return nil
}

// errMergeWithoutEnvironments is the error for --merge-environments set
// to true with no environments to merge.
var errMergeWithoutEnvironments = errors.New("--merge-environments merges the roots of the environments the configuration file names: add environments: to it, or leave the flag out")

// CheckMergeEnvironments rejects --merge-environments, or its setting in
// the file, set to true (merge, set) where envs, the file's environments,
// name none to merge: as CheckEnvironmentFlags does for the flags the
// environments can't go with.
func CheckMergeEnvironments(envs Environments, merge, set bool) error {
	if set && merge && len(envs) == 0 {
		return errMergeWithoutEnvironments
	}
	return nil
}

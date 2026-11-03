// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/sentence"
)

// SharedEnvironment is the environment of what no environment's tags
// place in an account, subscription or project several environments
// share, such as a network they all use. No environment of the file may
// take its name.
const SharedEnvironment = "shared"

// AllEnvironments stands for every environment where one is named, as in
// drift --environment all, so no environment of the file may take its
// name either.
const AllEnvironments = "all"

// Environments are the estate's environments, by name, such as dev and
// prod: the name is the directory of the environment's roots.
type Environments map[string]Environment

// Environment is where one environment is: the accounts, subscriptions or
// projects of the provider commands that hold it and, where one of them
// holds several environments, or for an environment that names none, the
// tags that tell its resources apart.
type Environment struct {
	// Sections are the provider commands' sections, by the command's
	// name, such as aws: { accounts: [...] }. What a section holds is the
	// provider's to say, in its profile (see harvest.Scope), so the format
	// has none of its own: Load checks each against the profile of the
	// command it names, and fails on one the binary isn't built with.
	Sections map[string]Section `yaml:",inline"`
	// Identities are, by the provider command's name, the identity its
	// section's scopes are read with: references to credentials, by the
	// keys its profile names (harvest.Scope.Identity), such as
	// assume_role. They are written beside the IDs in the section, as in
	// aws: { accounts: [...], assume_role: infraharvest-readonly }.
	Identities map[string]Identity `yaml:"-"`
	// Tags are tag keys and values (labels on Google Cloud) a resource of
	// the environment has, all of them, compared as --env-tags compares
	// environments: in lower case, with anything but letters, digits,
	// dots, dashes and underscores made a dash.
	Tags map[string]string `yaml:"tags,omitempty"`
	// Tokens are the words that stand for the environment inside
	// resources' names, such as prd for prod; by default its name.
	Tokens []string `yaml:"tokens,omitempty"`
}

// Identity is how one provider command reads an environment's scopes, by
// key (harvest.IdentityKey): a profile, a role, a service account, never
// a secret.
type Identity map[string]string

// UnmarshalYAML reads an environment: its tags and tokens, and a section
// for each provider command, in which a list holds the IDs of its scopes
// and a single value one key of the identity they are read with. Which
// keys a provider command has is its profile's to say (see checkSections).
func (e *Environment) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: an environment is a mapping", node.Line)
	}
	*e = Environment{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if seen[key] {
			return fmt.Errorf("line %d: key %q is set twice", node.Content[i].Line, key)
		}
		seen[key] = true
		switch key {
		case "tags":
			if err := value.Decode(&e.Tags); err != nil {
				return err
			}
		case "tokens":
			if err := value.Decode(&e.Tokens); err != nil {
				return err
			}
		default:
			section, identity, err := decodeSection(key, value)
			if err != nil {
				return err
			}
			if e.Sections == nil {
				e.Sections = map[string]Section{}
			}
			e.Sections[key] = section
			if len(identity) > 0 {
				if e.Identities == nil {
					e.Identities = map[string]Identity{}
				}
				e.Identities[key] = identity
			}
		}
	}
	return nil
}

// decodeSection reads the section of the provider command name: the lists
// of IDs, and the single values that name an identity.
func decodeSection(name string, node *yaml.Node) (Section, Identity, error) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return Section{}, nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("line %d: %s: a mapping, such as { accounts: [...] }", node.Line, name)
	}
	section, identity := Section{}, Identity{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if _, dup := section[key]; dup || identity[key] != "" {
			return nil, nil, fmt.Errorf("line %d: %s: key %q is set twice", node.Content[i].Line, name, key)
		}
		switch value.Kind {
		case yaml.ScalarNode:
			identity[key] = strings.TrimSpace(value.Value)
			if identity[key] == "" {
				return nil, nil, fmt.Errorf("line %d: %s: %s is empty", value.Line, name, key)
			}
		default:
			var ids []string
			if err := value.Decode(&ids); err != nil {
				return nil, nil, fmt.Errorf("line %d: %s: %s: %w", value.Line, name, key, err)
			}
			section[key] = ids
		}
	}
	return section, identity, nil
}

// Section is an environment's section for one provider command: the IDs
// of its accounts, subscriptions or projects, under the one key its
// profile names (harvest.Scope.Field), such as accounts.
type Section map[string][]string

// environmentName matches an environment's name, which names a directory.
// The GitHub Action checks its environment input with the same pattern.
var environmentName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Scopes returns the accounts, subscriptions or projects of e for the
// provider command provider, such as aws: the IDs of its section.
func (e Environment) Scopes(provider string) []string {
	section := e.Sections[provider]
	var ids []string
	for _, field := range slices.Sorted(maps.Keys(section)) {
		ids = append(ids, section[field]...)
	}
	return ids
}

// hasScopes reports whether e names any account, subscription or project.
func (e Environment) hasScopes() bool {
	for provider := range e.Sections {
		if len(e.Scopes(provider)) > 0 {
			return true
		}
	}
	return false
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
// project scope of the provider command whose profile is provider: those
// that name it; if none does, those that name no account, subscription
// or project and tell their resources by tags only. None means the
// scope's roots are laid out as without environments.
func (envs Environments) For(provider harvest.Profile, scope string) []string {
	var named, tagged []string
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		e := envs[name]
		switch {
		case slices.ContainsFunc(e.Scopes(provider.Command), func(s string) bool { return scopeKey(provider.Scope, s) == scopeKey(provider.Scope, scope) }):
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

// scopeKey is the ID of a scope of a provider whose scopes are s that
// is the same for every ID of that scope: in lower case where IDs are in
// either case, such as Azure's subscription GUIDs.
func scopeKey(s harvest.Scope, id string) string {
	if s.FoldCase {
		return strings.ToLower(id)
	}
	return id
}

// check returns why envs can't place every resource in one environment,
// if they can't: a name that isn't a directory name or is
// SharedEnvironment or AllEnvironments, an environment that names nothing
// to find it by, an empty account, subscription, project, tag or token,
// an ID that isn't one by its provider's profile, such as an AWS account
// that isn't 12 digits, a token two environments use, or an account,
// subscription or project several environments name, or several
// environments that name none, whose tags don't tell them apart (see
// apart), or a section that isn't one of a provider command the binary is
// built with, whose profile, of profiles, says what it holds (see
// checkSections).
func (envs Environments) check(profiles []harvest.Profile) error {
	profiles = slices.SortedFunc(slices.Values(profiles), func(a, b harvest.Profile) int { return cmp.Compare(a.Command, b.Command) })
	var fields, nouns []string
	for _, p := range profiles {
		fields = append(fields, p.Command+" "+p.Scope.Field)
		nouns = append(nouns, p.Scope.Noun)
	}
	tokens := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		e := envs[name]
		switch {
		case !environmentName.MatchString(name):
			return fmt.Errorf("%q: an environment's name is a directory's: lower-case letters, digits, dots, dashes and underscores", name)
		case name == SharedEnvironment:
			return fmt.Errorf("%q: the name is taken by what no environment's tags place", name)
		case name == AllEnvironments:
			return fmt.Errorf("%q: the name stands for every environment, as in drift --environment %s", name, AllEnvironments)
		}
		if err := e.checkSections(profiles); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !e.hasScopes() && len(e.Tags) == 0 {
			return fmt.Errorf("%s: name its %s, or its tags", name, sentence.List(fields, "or"))
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
	for _, p := range profiles {
		sharing := map[string][]string{}
		var scopes []string
		for _, name := range slices.Sorted(maps.Keys(envs)) {
			for _, scope := range envs[name].Scopes(p.Command) {
				key := scopeKey(p.Scope, scope)
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
				return fmt.Errorf("%s %s: %w", p.Command, scope, err)
			}
			if err := envs.sameIdentity(p.Command, sharing[scope]); err != nil {
				return fmt.Errorf("%s %s: %w", p.Command, scope, err)
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
		return fmt.Errorf("the environments that name no %s: %w", sentence.List(nouns, "or"), err)
	}
	return nil
}

// checkSections returns why a section of e isn't one of the provider
// commands whose profiles are profiles: the section of a command none of
// them has, a key other than the IDs' of its profile, such as account
// for accounts, or an ID that is empty or isn't one by its profile, such
// as an AWS account that isn't 12 digits.
func (e Environment) checkSections(profiles []harvest.Profile) error {
	for command := range e.Identities {
		if _, ok := e.Sections[command]; !ok {
			return fmt.Errorf("%s: identity without a section", command)
		}
	}
	for _, command := range slices.Sorted(maps.Keys(e.Sections)) {
		i := slices.IndexFunc(profiles, func(p harvest.Profile) bool { return p.Command == command })
		if i < 0 {
			return unknownProvider(command, profiles)
		}
		p := profiles[i]
		for _, field := range slices.Sorted(maps.Keys(e.Sections[command])) {
			switch {
			case field == p.Scope.Field:
			case p.Scope.IdentityKey(field) != nil:
				return fmt.Errorf("%s: %s takes one value, not a list", command, field)
			default:
				return fmt.Errorf("%s: unknown key %q: use %s", command, field, strings.Join(p.Scope.Keys(), ", "))
			}
		}
		if err := e.checkIdentity(command, p); err != nil {
			return err
		}
		for _, scope := range e.Scopes(command) {
			switch {
			case strings.TrimSpace(scope) == "":
				return fmt.Errorf("%s: an empty ID", command)
			case p.Scope.ID != nil && !p.Scope.ID.MatchString(scope):
				return fmt.Errorf("%s: %q isn't %s", command, scope, p.Scope.IDFormat)
			}
		}
	}
	return nil
}

// checkIdentity returns why the identity e's section for the provider
// command whose profile is p names isn't one: a key other than the
// profile's (harvest.Scope.Identity), the scope's field with a single
// value, a value that isn't of the key's format or looks like a secret
// (the file holds references, never credentials), or an identity for
// scopes the environment doesn't name, which would read nothing.
func (e Environment) checkIdentity(command string, p harvest.Profile) error {
	identity := e.Identities[command]
	if len(identity) == 0 {
		return nil
	}
	if _, ok := identity[p.Scope.Field]; ok {
		return fmt.Errorf("%s: %s is a list of IDs", command, p.Scope.Field)
	}
	if len(e.Scopes(command)) == 0 {
		return fmt.Errorf("%s: %s read the %s the environment names: name its %s", command, sentence.List(slices.Sorted(maps.Keys(identity)), "and"), p.Scope.Noun, p.Scope.Field)
	}
	for _, key := range slices.Sorted(maps.Keys(identity)) {
		value := identity[key]
		k := p.Scope.IdentityKey(key)
		switch {
		case key == p.Scope.Field:
			return fmt.Errorf("%s: %s is a list of IDs", command, key)
		case k == nil:
			return fmt.Errorf("%s: unknown key %q: use %s", command, key, strings.Join(p.Scope.Keys(), ", "))
		case looksSecret(value):
			return fmt.Errorf("%s: %s: the value looks like a secret: name the identity, such as a profile, a role or a service account, never a credential", command, key)
		case k.Match != nil && !k.Match.MatchString(value):
			return fmt.Errorf("%s: %s: %q isn't %s", command, key, value, k.Format)
		}
		if k.Check != nil {
			if err := k.Check(value, e.Scopes(command)); err != nil {
				return fmt.Errorf("%s: %s: %w", command, key, err)
			}
		}
	}
	return nil
}

// secretShapes are values that are credentials, which no identity of the
// file is: a private key, an AWS access key ID or a 40-character secret
// access key, a JWT or Google OAuth access token, a GitHub token, and an
// Azure client secret.
var secretShapes = regexp.MustCompile(`-----BEGIN|^\{|^(AKIA|ASIA|AGPA|AIDA|AROA)[A-Z0-9]{12,}$|^[A-Za-z0-9/+=]{40}$|^eyJ[\w-]+\.[\w-]+\.|^ya29\.|^gh[pousr]_|^[A-Za-z0-9_.-]{3}[0-9]Q~[A-Za-z0-9_.~-]{20,}$`)

// looksSecret reports whether value has the shape of a credential.
func looksSecret(value string) bool {
	return secretShapes.MatchString(strings.TrimSpace(value))
}

// sameIdentity returns why the environments names, which name the same
// account, subscription or project, would read it with different
// identities, if they do: it is read once.
func (envs Environments) sameIdentity(command string, names []string) error {
	for _, name := range names[1:] {
		if !maps.Equal(envs[names[0]].Identities[command], envs[name].Identities[command]) {
			return fmt.Errorf("%s and %s name it, and are read with different identities: give them the same", names[0], name)
		}
	}
	return nil
}

// IdentityFor returns the identity the scope of the provider command
// whose profile is provider is read with, by key (see
// harvest.Scope.Identity): that of the environments that name it, which
// the file's check leaves the same. None means the ambient credentials.
func (envs Environments) IdentityFor(provider harvest.Profile, scope string) Identity {
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		e := envs[name]
		if len(e.Identities[provider.Command]) > 0 && slices.ContainsFunc(e.Scopes(provider.Command), func(s string) bool { return scopeKey(provider.Scope, s) == scopeKey(provider.Scope, scope) }) {
			return e.Identities[provider.Command]
		}
	}
	return nil
}

// unknownProvider is the error of a section, of an environment or of
// providers:, for the provider command name, which none of the provider
// commands the binary is built with, whose profiles are profiles, is. It
// may be a provider the build left out, as -tags minimal,aws leaves out
// the others: that fails, naming the build tag that adds it, as reading
// a managed-state backend whose store isn't built in does, rather than
// go unread.
func unknownProvider(name string, profiles []harvest.Profile) error {
	commands := make([]string, 0, len(profiles))
	for _, p := range profiles {
		commands = append(commands, p.Command)
	}
	slices.Sort(commands)
	return fmt.Errorf("unknown provider %q: use %s; for a provider this infraharvest was built without, build it with -tags minimal,%s, or without minimal", name, strings.Join(commands, ", "), name)
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

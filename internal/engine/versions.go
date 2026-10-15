// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// registryTimeout bounds one registry request; registryBodyLimit bounds
// what the registry's answer may hold.
const (
	registryTimeout   = 30 * time.Second
	registryBodyLimit = 4 << 20
)

// DefaultRegistry is the registry provider sources without a host use.
const DefaultRegistry = "registry.terraform.io"

// BinaryVersion returns the version of the Terraform or OpenTofu binary at
// execPath.
func BinaryVersion(ctx context.Context, execPath string) (*version.Version, error) {
	return binaryVersion(ctx, execPath)
}

// RequiredVersion is the required_version for configuration generated with
// Terraform v: this minor release or newer, within its major release.
func RequiredVersion(v *version.Version) string {
	s := v.Segments()
	return fmt.Sprintf(">= %d.%d, < %d.0", s[0], s[1], s[0]+1)
}

// ProviderConstraint pins a provider to v's minor release line: "~> 6.67"
// accepts 6.67 and later 6.x releases, never 7.0.
func ProviderConstraint(v *version.Version) string {
	s := v.Segments()
	return fmt.Sprintf("~> %d.%d", s[0], s[1])
}

// registryAttempts is how many times a registry lookup is tried when the
// connection fails or the registry answers 429 or 5xx; registryBackoff is
// the wait before the second attempt, doubled before each later one.
const registryAttempts = 3

var registryBackoff = time.Second

// registryAnswers remembers the registry's answers by URL: one import asks
// once, however many accounts and regions it covers, and every root it
// generates pins the same release.
var registryAnswers sync.Map

// getRegistry returns what the registry answers at url, what names the
// lookup in errors, retrying a failed connection, a 429 or a 5xx answer.
func getRegistry(ctx context.Context, client *http.Client, url, what string) ([]byte, error) {
	if content, ok := registryAnswers.Load(url); ok {
		return content.([]byte), nil
	}
	var content []byte
	var err error
	wait := registryBackoff
	for attempt := 1; ; attempt++ {
		var retry bool
		content, retry, err = fetchVersions(ctx, client, url)
		if err == nil || !retry || attempt == registryAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("look up %s: %w", what, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", what, err)
	}
	registryAnswers.Store(url, content)
	return content, nil
}

// LatestProviderVersion asks the registry for the newest release of the
// provider at source ("hashicorp/aws" or "registry.terraform.io/hashicorp/aws"),
// leaving out prereleases. baseURL replaces https://<host> when not empty,
// for tests.
func LatestProviderVersion(ctx context.Context, client *http.Client, baseURL, source string) (*version.Version, error) {
	return ProviderVersion(ctx, client, baseURL, source, nil)
}

// ProviderVersion is LatestProviderVersion within constraints: the newest
// release every one of them accepts.
func ProviderVersion(ctx context.Context, client *http.Client, baseURL, source string, constraints []version.Constraints) (*version.Version, error) {
	parts := strings.Split(source, "/")
	if len(parts) == 2 {
		parts = append([]string{DefaultRegistry}, parts...)
	}
	if len(parts) != 3 {
		return nil, fmt.Errorf("provider source %q is not [host/]namespace/type", source)
	}
	if baseURL == "" {
		baseURL = "https://" + parts[0]
	}
	url := fmt.Sprintf("%s/v1/providers/%s/%s/versions", baseURL, parts[1], parts[2])
	what := source + " versions"
	content, err := getRegistry(ctx, client, url, what)
	if err != nil {
		return nil, err
	}
	var body struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(content, &body); err != nil {
		return nil, fmt.Errorf("look up %s: %w", what, err)
	}
	var latest *version.Version
	released := false
	for _, entry := range body.Versions {
		v, err := version.NewVersion(entry.Version)
		if err != nil || v.Prerelease() != "" {
			continue
		}
		released = true
		if !accepts(constraints, v) {
			continue
		}
		if latest == nil || v.GreaterThan(latest) {
			latest = v
		}
	}
	switch {
	case !released:
		return nil, fmt.Errorf("look up %s: %s lists no releases", what, url)
	case latest == nil:
		return nil, fmt.Errorf("look up %s: no release meets %s", what, joinConstraints(constraints))
	}
	return latest, nil
}

// accepts reports whether every one of constraints accepts v.
func accepts(constraints []version.Constraints, v *version.Version) bool {
	for _, c := range constraints {
		if !c.Check(v) {
			return false
		}
	}
	return true
}

func joinConstraints(constraints []version.Constraints) string {
	s := make([]string, len(constraints))
	for i, c := range constraints {
		s[i] = c.String()
	}
	return strings.Join(s, " and ")
}

// ModuleRelease is a release of a registry module, with the versions of
// each provider it accepts, by provider source (such as hashicorp/aws).
type ModuleRelease struct {
	Version   *version.Version
	Providers map[string]version.Constraints
}

// ModuleReleases lists the releases of the registry module at source
// (namespace/name/system, with //path for a submodule, as in a module
// call's source), newest first, leaving out prereleases. Each has the
// provider versions the module, or the submodule, accepts. baseURL
// replaces https://registry.terraform.io when not empty, for tests.
func ModuleReleases(ctx context.Context, client *http.Client, baseURL, source string) ([]ModuleRelease, error) {
	module, subdir, _ := strings.Cut(source, "//")
	if strings.Count(module, "/") != 2 {
		return nil, fmt.Errorf("module source %q is not namespace/name/system", source)
	}
	if baseURL == "" {
		baseURL = "https://" + DefaultRegistry
	}
	what := source + " versions"
	content, err := getRegistry(ctx, client, baseURL+"/v1/modules/"+module+"/versions", what)
	if err != nil {
		return nil, err
	}
	type providers []struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	var body struct {
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
				Root    struct {
					Providers providers `json:"providers"`
				} `json:"root"`
				Submodules []struct {
					Path      string    `json:"path"`
					Providers providers `json:"providers"`
				} `json:"submodules"`
			} `json:"versions"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(content, &body); err != nil {
		return nil, fmt.Errorf("look up %s: %w", what, err)
	}
	var releases []ModuleRelease
	for _, m := range body.Modules {
		for _, entry := range m.Versions {
			v, err := version.NewVersion(entry.Version)
			if err != nil || v.Prerelease() != "" {
				continue
			}
			ps := entry.Root.Providers
			if subdir != "" {
				ps = nil
				for _, s := range entry.Submodules {
					if s.Path == subdir {
						ps = s.Providers
					}
				}
			}
			release := ModuleRelease{Version: v, Providers: map[string]version.Constraints{}}
			for _, p := range ps {
				if p.Version == "" {
					continue
				}
				c, err := version.NewConstraint(p.Version)
				if err != nil {
					continue
				}
				source := p.Source
				if source == "" {
					source = "hashicorp/" + p.Name
				}
				release.Providers[strings.ToLower(source)] = c
			}
			releases = append(releases, release)
		}
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("look up %s: no releases", what)
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].Version.GreaterThan(releases[j].Version) })
	return releases, nil
}

// fetchVersions returns what the registry answers at url, within
// registryTimeout: a registry that hangs must not hold the import up for
// good. retry says whether another attempt may succeed: after a failed
// connection or a timeout, or a 429 or 5xx answer.
func fetchVersions(ctx context.Context, client *http.Client, url string) (content []byte, retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, !errors.Is(err, context.Canceled), err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	content, err = io.ReadAll(io.LimitReader(resp.Body, registryBodyLimit))
	return content, err != nil, err
}

// LockedVersion returns the version of the provider at source
// (host/namespace/type) that a dependency lock file records, or "" if it
// records none.
func LockedVersion(lock []byte, source string) string {
	file, diags := hclsyntax.ParseConfig(lock, LockFileName, hcl.InitialPos)
	if diags.HasErrors() {
		return ""
	}
	for _, b := range file.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 || !strings.EqualFold(b.Labels[0], source) {
			continue
		}
		if attr, ok := b.Body.Attributes["version"]; ok {
			if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String && !v.IsNull() {
				return v.AsString()
			}
		}
	}
	return ""
}

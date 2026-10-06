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

// latestVersions remembers the registry's answers by URL: one import asks
// once, however many accounts and regions it covers, and every root it
// generates pins the same release.
var latestVersions sync.Map

// LatestProviderVersion asks the registry for the newest release of the
// provider at source ("hashicorp/aws" or "registry.terraform.io/hashicorp/aws"),
// leaving out prereleases. baseURL replaces https://<host> when not empty,
// for tests.
func LatestProviderVersion(ctx context.Context, client *http.Client, baseURL, source string) (*version.Version, error) {
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
	if v, ok := latestVersions.Load(url); ok {
		return v.(*version.Version), nil
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
			return nil, fmt.Errorf("look up %s versions: %w", source, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
	if err != nil {
		return nil, fmt.Errorf("look up %s versions: %w", source, err)
	}
	var body struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(content, &body); err != nil {
		return nil, fmt.Errorf("look up %s versions: %w", source, err)
	}
	var latest *version.Version
	for _, entry := range body.Versions {
		v, err := version.NewVersion(entry.Version)
		if err != nil || v.Prerelease() != "" {
			continue
		}
		if latest == nil || v.GreaterThan(latest) {
			latest = v
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("look up %s versions: %s lists no releases", source, url)
	}
	latestVersions.Store(url, latest)
	return latest, nil
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

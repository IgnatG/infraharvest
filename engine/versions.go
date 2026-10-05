// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	// A registry that hangs must not hold the import up for good.
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	url := fmt.Sprintf("%s/v1/providers/%s/%s/versions", baseURL, parts[1], parts[2])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("look up %s versions: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("look up %s versions: %s returned %s", source, url, resp.Status)
	}
	var body struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, registryBodyLimit)).Decode(&body); err != nil {
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
	return latest, nil
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

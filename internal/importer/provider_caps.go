// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	goversion "github.com/hashicorp/go-version"
)

// providerCapsJSON holds the caps as data, so that the schema-metadata
// workflow reads the same file (.github/workflows/schema-metadata-generate.yml):
// an object by the provider's namespace and type, whose entries hold
// "below", the release a provider is held under, and "reason".
//
//go:embed provider_caps.json
var providerCapsJSON []byte

// providerCap holds a provider below a release that can't import: from
// below on, `plan -generate-config-out` writes configuration Terraform
// rejects. A root is then pinned to the newest release under it.
type providerCap struct {
	below  *goversion.Version
	reason string
}

// providerCaps are the caps, by the provider's namespace and type, in
// any registry, as provider_caps.json lists them. Remove one once a
// release that imports again is out, and check that release in the
// end-to-end tests first.
//
// For hashicorp/aws, terraform-plugin-sdk 2.41 looks the attributes up in
// a resource's Schema, which the AWS provider leaves empty for its
// SchemaFunc, so it returns the whole state as configuration: computed
// attributes such as arn, the id, and both of name and name_prefix.
var providerCaps = mustParseProviderCaps(providerCapsJSON)

// parseProviderCaps reads the caps from data, and fails on an entry
// whose release isn't a version or whose reason is empty.
func parseProviderCaps(data []byte) (map[string]providerCap, error) {
	var raw map[string]struct {
		Below  string `json:"below"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("provider caps: %w", err)
	}
	caps := make(map[string]providerCap, len(raw))
	for source, entry := range raw {
		if parts := strings.Split(source, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" || source != strings.ToLower(source) {
			return nil, fmt.Errorf("provider caps: %q isn't a lower-case namespace/type", source)
		}
		below, err := goversion.NewVersion(entry.Below)
		if err != nil {
			return nil, fmt.Errorf("provider caps: %s: below: %w", source, err)
		}
		if strings.TrimSpace(entry.Reason) == "" {
			return nil, fmt.Errorf("provider caps: %s: no reason", source)
		}
		caps[source] = providerCap{below: below, reason: entry.Reason}
	}
	return caps, nil
}

func mustParseProviderCaps(data []byte) map[string]providerCap {
	caps, err := parseProviderCaps(data)
	if err != nil {
		panic(err)
	}
	return caps
}

// capOf returns the cap of the provider at source, with or without its
// registry's host, if it has one.
func capOf(source string) (providerCap, bool) {
	parts := strings.Split(strings.ToLower(source), "/")
	if len(parts) < 2 {
		return providerCap{}, false
	}
	c, ok := providerCaps[parts[len(parts)-2]+"/"+parts[len(parts)-1]]
	return c, ok
}

// constraint is the version constraint that keeps a provider under c.
func (c providerCap) constraint() goversion.Constraints {
	return goversion.MustConstraints(goversion.NewConstraint("< " + c.below.String()))
}

// excludes reports whether c holds the provider back from v.
func (c providerCap) excludes(v *goversion.Version) bool {
	return !v.LessThan(c.below)
}

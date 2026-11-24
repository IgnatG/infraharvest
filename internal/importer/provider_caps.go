// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"strings"

	goversion "github.com/hashicorp/go-version"
)

// providerCap holds a provider below a release that can't import: from
// below on, `plan -generate-config-out` writes configuration Terraform
// rejects. A root is then pinned to the newest release under it.
type providerCap struct {
	below  *goversion.Version
	reason string
}

// providerCaps are the caps, by the provider's namespace and type, in
// any registry. Remove one once a release that imports again is out, and
// check that release in the end-to-end tests first.
var providerCaps = map[string]providerCap{
	// terraform-plugin-sdk 2.41 looks the attributes up in a resource's
	// Schema, which the AWS provider leaves empty for its SchemaFunc, so
	// it returns the whole state as configuration: computed attributes
	// such as arn, the id, and both of name and name_prefix.
	"hashicorp/aws": {
		below:  goversion.Must(goversion.NewVersion("6.74.0")),
		reason: "6.74.0 and later generate configuration with every attribute of the state, which Terraform rejects",
	},
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

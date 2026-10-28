// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
)

// A catalogue default for an argument the provider's code gives a default
// of its own must be that default, for the release the schema metadata was
// generated from, the one generated roots pin: the provider sends its own
// when the configuration leaves the argument out, so another value never
// plans the same on import, and drift would take a deployed value equal to
// the catalogue's for one the configuration can leave out.
func TestDefaultsAgreeWithProviderDefaults(t *testing.T) {
	providers, err := Providers()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		meta, err := schemameta.Builtin(provider)
		if err != nil {
			t.Fatal(err)
		}
		if meta == nil {
			continue
		}
		v := version.Must(version.NewVersion(meta.Version))
		c := MustBuiltin(provider)
		defaults := c.Defaults(v)
		for _, typ := range slices.Sorted(maps.Keys(defaults)) {
			for _, d := range defaults[typ] {
				path, value, _ := strings.Cut(d, "=")
				argument := meta.Argument(typ, path)
				if argument == nil || argument.Default == nil {
					continue
				}
				if got := fmt.Sprint(argument.Default); got != value {
					t.Errorf("%s: %s: default %s=%s, but %s %s defaults it to %s", c.Source(typ), typ, path, value, provider, meta.Version, got)
				}
			}
		}
	}
}

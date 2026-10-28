// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"strings"

	"github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// newestProvider returns the root of roots, the environments' roots of a
// stack, whose provider the merged root pins: the roots' provider, by
// name and source, at the newest release line any of them pins, such as
// ~> 6.14 of ~> 6.10 and ~> 6.14, which the gate then plans every
// environment with (see MergeRoots), its lock file too. It fails where
// the roots use different providers, or pin different major releases of
// the provider, whose resources' arguments differ: the environments then
// keep their roots.
func newestProvider(roots []envRoot) (int, error) {
	if len(roots) == 0 {
		return 0, fmt.Errorf("no roots to merge")
	}
	newest := 0
	var newestVersion *version.Version
	for i, root := range roots {
		p := root.provider
		if first := roots[0].provider; p.Name != first.Name || p.Source != first.Source {
			return 0, fmt.Errorf("the environments use different providers: %s and %s", first.Source, p.Source)
		}
		if p.Version == "" {
			continue
		}
		bound, err := rootfile.LowerBound(p.Version)
		if err != nil {
			return 0, fmt.Errorf("%s: provider version %q: %w", root.env, p.Version, err)
		}
		v, err := version.NewVersion(strings.TrimPrefix(bound, ">= "))
		if err != nil {
			return 0, fmt.Errorf("%s: provider version %q: %w", root.env, p.Version, err)
		}
		switch {
		case newestVersion == nil:
			newest, newestVersion = i, v
		case v.Segments()[0] != newestVersion.Segments()[0]:
			return 0, fmt.Errorf("the environments pin different major releases of %s: %s (%s) and %s (%s)", p.Source, roots[newest].provider.Version, roots[newest].env, p.Version, root.env)
		case v.GreaterThan(newestVersion):
			newest, newestVersion = i, v
		}
	}
	return newest, nil
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// requireMinimumVersion sets the required_version of the root in dir to
// the oldest release its configuration works with: at least
// rootfile.MinimumVersion, the release that brought each feature it uses,
// or OpenTofu's where later (see rootfile.FeaturesNeed), and the lowest
// version each module it calls, as Terraform installed them, requires. It
// stays within that release's major version.
func requireMinimumVersion(dir string) error {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return err
	}
	need := rootfile.FeaturesNeed(files)
	raise := func(v *version.Version) {
		if v != nil && v.GreaterThan(need) {
			need = v
		}
	}
	modules, err := tfrun.InstalledModules(dir, "")
	if err != nil {
		return err
	}
	// Only the calls the root still has: Terraform installed those of
	// trials the plan rejected too (see synthesize).
	called := map[string]bool{}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type == "module" && len(b.Labels) == 1 {
				called[b.Labels[0]] = true
			}
		}
	}
	for _, m := range modules {
		if top, _, _ := strings.Cut(m.Key, "."); called[top] {
			raise(requiredByModule(m.Dir))
		}
	}
	path := filepath.Join(dir, rootfile.VersionsFileName)
	versions, err := hclfile.LoadOptional(path)
	if err != nil || versions == nil {
		return err
	}
	want := fmt.Sprintf(">= %s, < %d.0", rootfile.ShortVersion(need), need.Segments()[0]+1)
	changed := false
	blocks := versions.Edit().Body().Blocks()
	for i, b := range versions.Syntax.Blocks {
		if _, ok := b.Body.Attributes["required_version"]; b.Type != "terraform" || !ok || i >= len(blocks) || hclfile.StringAttribute(b.Body, "required_version") == want {
			continue
		}
		blocks[i].Body().SetAttributeValue("required_version", cty.StringVal(want))
		changed = true
	}
	if !changed {
		return nil
	}
	return versions.Save()
}

// requiredByModule returns the lowest Terraform version the module in dir
// requires, or nil if it says none.
func requiredByModule(dir string) *version.Version {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		// A module infraharvest can't read is Terraform's to judge.
		return nil
	}
	var need *version.Version
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			constraint := hclfile.StringAttribute(b.Body, "required_version")
			if b.Type != "terraform" || constraint == "" {
				continue
			}
			lower, err := rootfile.LowerBound(constraint)
			if err != nil {
				continue
			}
			v, err := version.NewVersion(strings.TrimPrefix(lower, ">= "))
			if err == nil && (need == nil || v.GreaterThan(need)) {
				need = v
			}
		}
	}
	return need
}

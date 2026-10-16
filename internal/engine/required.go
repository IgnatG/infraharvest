// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// MinimumVersion is the oldest Terraform release generated configuration
// works with: import blocks came with 1.5. OpenTofu has had them from its
// first release, 1.6.
var MinimumVersion = version.Must(version.NewVersion("1.5.0"))

// features are the Terraform releases that brought what configuration may
// use beyond MinimumVersion, each with how it shows in a file's blocks.
var features = []struct {
	since string
	uses  func(b *hclsyntax.Block) bool
}{
	// Import blocks with for_each, and removed blocks.
	{"1.7.0", func(b *hclsyntax.Block) bool {
		_, forEach := b.Body.Attributes["for_each"]
		return b.Type == "removed" || (b.Type == "import" && forEach)
	}},
	// Provider-defined functions.
	{"1.8.0", func(b *hclsyntax.Block) bool { return callsProviderFunction(b.Body) }},
	// Ephemeral variables and resources.
	{"1.10.0", func(b *hclsyntax.Block) bool {
		if b.Type == "ephemeral" {
			return true
		}
		attr, ok := b.Body.Attributes["ephemeral"]
		if b.Type != "variable" || !ok {
			return false
		}
		v, diags := attr.Expr.Value(nil)
		return !diags.HasErrors() && v.Type() == cty.Bool && v.True()
	}},
	// Write-only arguments, such as an SSM parameter's value_wo.
	{"1.11.0", func(b *hclsyntax.Block) bool { return b.Type == "resource" && setsWriteOnly(b.Body) }},
	// Import blocks by identity.
	{"1.12.0", func(b *hclsyntax.Block) bool {
		_, identity := b.Body.Attributes["identity"]
		return b.Type == "import" && identity
	}},
}

// requireMinimumVersion sets the required_version of the root in dir to
// the oldest Terraform release its configuration works with: at least
// MinimumVersion, the release that brought each feature it uses (see
// features), and the lowest version each module it calls, as Terraform
// installed them, requires. It stays within that release's major version.
func requireMinimumVersion(dir string) error {
	files, err := configFiles(dir)
	if err != nil {
		return err
	}
	need := MinimumVersion
	raise := func(v *version.Version) {
		if v != nil && v.GreaterThan(need) {
			need = v
		}
	}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			for _, feature := range features {
				if feature.uses(b) {
					raise(version.Must(version.NewVersion(feature.since)))
				}
			}
		}
	}
	modules, err := installedModules(dir)
	if err != nil {
		return err
	}
	for _, m := range modules {
		raise(requiredByModule(m.dir))
	}
	path := filepath.Join(dir, VersionsFileName)
	versions, err := loadOptionalHCL(path)
	if err != nil || versions == nil {
		return err
	}
	want := fmt.Sprintf(">= %s, < %d.0", shortVersion(need), need.Segments()[0]+1)
	changed := false
	blocks := versions.file.Body().Blocks()
	for i, b := range versions.syntax.Blocks {
		if _, ok := b.Body.Attributes["required_version"]; b.Type != "terraform" || !ok || i >= len(blocks) || stringAttribute(b.Body, "required_version") == want {
			continue
		}
		blocks[i].Body().SetAttributeValue("required_version", cty.StringVal(want))
		changed = true
	}
	if !changed {
		return nil
	}
	return versions.save()
}

// shortVersion writes v as major.minor, with the patch only if it isn't 0.
func shortVersion(v *version.Version) string {
	s := v.Segments()
	if s[2] == 0 {
		return fmt.Sprintf("%d.%d", s[0], s[1])
	}
	return fmt.Sprintf("%d.%d.%d", s[0], s[1], s[2])
}

// installedModule is a module call Terraform installed for a root: its
// key, such as vpc or vpc.nat for a module that one calls, and the
// directory it is in.
type installedModule struct {
	key, dir string
}

// installedModules returns the modules Terraform installed for the root in
// dir, as its modules.json lists them; none if it installed none.
func installedModules(dir string) ([]installedModule, error) {
	content, err := os.ReadFile(filepath.Join(dir, ".terraform", "modules", "modules.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Modules []struct {
			Key string `json:"Key"`
			Dir string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, fmt.Errorf("modules.json: %w", err)
	}
	var modules []installedModule
	for _, m := range manifest.Modules {
		if m.Key != "" && m.Dir != "" {
			modules = append(modules, installedModule{key: m.Key, dir: filepath.Join(dir, filepath.FromSlash(m.Dir))})
		}
	}
	return modules, nil
}

// requiredByModule returns the lowest Terraform version the module in dir
// requires, or nil if it says none.
func requiredByModule(dir string) *version.Version {
	files, err := configFiles(dir)
	if err != nil {
		// A module infraharvest can't read is Terraform's to judge.
		return nil
	}
	var need *version.Version
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			constraint := stringAttribute(b.Body, "required_version")
			if b.Type != "terraform" || constraint == "" {
				continue
			}
			lower, err := lowerBound(constraint)
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

// callsProviderFunction reports whether a body, or its nested blocks,
// calls a provider-defined function, such as provider::aws::arn_parse.
func callsProviderFunction(body *hclsyntax.Body) bool {
	found := false
	_ = hclsyntax.VisitAll(body, func(n hclsyntax.Node) hcl.Diagnostics {
		if call, ok := n.(*hclsyntax.FunctionCallExpr); ok && strings.HasPrefix(call.Name, "provider::") {
			found = true
		}
		return nil
	})
	return found
}

// setsWriteOnly reports whether a resource's body, or its nested blocks,
// sets a write-only argument, which the providers name *_wo.
func setsWriteOnly(body *hclsyntax.Body) bool {
	for name, attr := range body.Attributes {
		if strings.HasSuffix(name, "_wo") && !isNullLiteral(attr.Expr) {
			return true
		}
	}
	for _, b := range body.Blocks {
		if setsWriteOnly(b.Body) {
			return true
		}
	}
	return false
}

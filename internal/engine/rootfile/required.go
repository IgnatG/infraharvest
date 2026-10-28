// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package rootfile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// minimumVersion is the oldest Terraform release generated configuration
// works with: import blocks came with 1.5. OpenTofu has had them from its
// first release, 1.6.
const minimumVersion = "1.5.0"

// MinimumVersion returns the oldest Terraform release generated
// configuration works with (see minimumVersion), a value of the caller's
// own.
func MinimumVersion() *version.Version {
	return version.Must(version.NewVersion(minimumVersion))
}

// features are the Terraform releases that brought what configuration may
// use beyond MinimumVersion, each with how it shows in a file's blocks,
// and the OpenTofu release, where it came later. Both check
// required_version against their own version, so a root that uses a
// feature requires the later of the two.
var features = []struct {
	since, tofu string
	uses        func(b *hclsyntax.Block) bool
}{
	// Import blocks whose ID is an expression, such as a variable.
	{"1.6.0", "", func(b *hclsyntax.Block) bool {
		id, ok := b.Body.Attributes["id"]
		return b.Type == "import" && ok && len(id.Expr.Variables()) > 0
	}},
	// Import blocks with for_each, and removed blocks.
	{"1.7.0", "", func(b *hclsyntax.Block) bool {
		_, forEach := b.Body.Attributes["for_each"]
		return b.Type == "removed" || (b.Type == "import" && forEach)
	}},
	// Removed blocks with a lifecycle block, as destroy = false: OpenTofu
	// took them from 1.10.
	{"1.7.0", "1.10.0", func(b *hclsyntax.Block) bool {
		return b.Type == "removed" && slices.ContainsFunc(b.Body.Blocks, func(l *hclsyntax.Block) bool { return l.Type == "lifecycle" })
	}},
	// Provider-defined functions.
	{"1.8.0", "", func(b *hclsyntax.Block) bool { return callsProviderFunction(b.Body) }},
	// Ephemeral variables and resources.
	{"1.10.0", "", func(b *hclsyntax.Block) bool {
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
	{"1.11.0", "", func(b *hclsyntax.Block) bool { return b.Type == "resource" && setsWriteOnly(b.Body) }},
	// Import blocks by identity.
	{"1.12.0", "", func(b *hclsyntax.Block) bool {
		_, identity := b.Body.Attributes["identity"]
		return b.Type == "import" && identity
	}},
}

// FeaturesNeed returns the oldest release the blocks of files work with:
// MinimumVersion, or the release that brought a feature they use, or
// OpenTofu's where later (see features).
func FeaturesNeed(files []*hclfile.File) *version.Version {
	need := MinimumVersion()
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			for _, feature := range features {
				if !feature.uses(b) {
					continue
				}
				for _, since := range []string{feature.since, feature.tofu} {
					if v, err := version.NewVersion(since); err == nil && v.GreaterThan(need) {
						need = v
					}
				}
			}
		}
	}
	return need
}

// ShortVersion writes v as major.minor, with the patch only if it isn't 0.
func ShortVersion(v *version.Version) string {
	s := v.Segments()
	if s[2] == 0 {
		return fmt.Sprintf("%d.%d", s[0], s[1])
	}
	return fmt.Sprintf("%d.%d.%d", s[0], s[1], s[2])
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
		if strings.HasSuffix(name, "_wo") && !hclfile.IsNullLiteral(attr.Expr) {
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

// RaiseRequiredVersion raises the required_version of the root in dir,
// in whichever of its files has it, such as a merged root's providers.tf,
// to the oldest release its blocks work with (see FeaturesNeed), where
// that is later: as for the removed blocks a merged root gets once
// written (see envmerge.WriteMergedMoves). Unlike the engine's
// requireMinimumVersion, it never lowers it, as what the root's modules
// need isn't known here.
func RaiseRequiredVersion(dir string) error {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return err
	}
	need := FeaturesNeed(files)
	want := fmt.Sprintf(">= %s, < %d.0", ShortVersion(need), need.Segments()[0]+1)
	for _, f := range files {
		blocks := f.Edit().Body().Blocks()
		changed := false
		for i, b := range f.Syntax.Blocks {
			if _, ok := b.Body.Attributes["required_version"]; b.Type != "terraform" || !ok || i >= len(blocks) {
				continue
			}
			if lower, err := LowerBound(hclfile.StringAttribute(b.Body, "required_version")); err == nil {
				if v, err := version.NewVersion(strings.TrimPrefix(lower, ">= ")); err == nil && !need.GreaterThan(v) {
					continue
				}
			}
			blocks[i].Body().SetAttributeValue("required_version", cty.StringVal(want))
			changed = true
		}
		if changed {
			if err := f.Save(); err != nil {
				return err
			}
		}
	}
	return nil
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

// LowerBound renders a version constraint as its lower bound only, as
// ">= 6.14": the highest version its =, >=, ~> and > parts require. It
// fails when the constraint doesn't parse or has no such part.
func LowerBound(constraint string) (string, error) {
	cs, err := version.NewConstraint(constraint)
	if err != nil {
		return "", err
	}
	var bound *version.Version
	for _, c := range cs {
		text := strings.TrimSpace(c.String())
		op := ""
		for _, candidate := range []string{">=", "<=", "!=", "~>", "=", ">", "<"} {
			if strings.HasPrefix(text, candidate) {
				op = candidate
				break
			}
		}
		switch op {
		case "<", "<=", "!=":
			continue
		}
		v, err := version.NewVersion(strings.TrimSpace(strings.TrimPrefix(text, op)))
		if err != nil {
			return "", err
		}
		if bound == nil || v.GreaterThan(bound) {
			bound = v
		}
	}
	if bound == nil {
		return "", fmt.Errorf("%q has no lower bound", constraint)
	}
	return ">= " + bound.Original(), nil
}

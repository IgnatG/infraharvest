// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// maxRepairRounds bounds the validate-and-repair loop. Most configurations
// need one round; removing an attribute can reveal another error.
const maxRepairRounds = 5

// repair makes the configuration Terraform generated into path valid where
// that is safe: it applies the provider's fixup (optional), then removes the
// attributes validation rejects (see removeRejected) until the configuration
// validates or nothing more can be removed. It reports whether it changed
// the file.
func repair(ctx context.Context, tf Terraform, path string, fixup Fixup) (bool, error) {
	repaired := false
	if fixup != nil {
		changed, err := rewrite(path, func(f *hclwrite.File, _ *hclsyntax.Body) bool {
			changed := false
			for _, block := range f.Body().Blocks() {
				if block.Type() == "resource" && len(block.Labels()) == 2 && fixup(block.Labels()[0], block.Body()) {
					changed = true
				}
			}
			return changed
		})
		if err != nil {
			return false, err
		}
		repaired = changed
	}
	for range maxRepairRounds {
		out, err := tf.Validate(ctx)
		if err != nil {
			return repaired, fmt.Errorf("terraform validate: %w", err)
		}
		if out.Valid {
			break
		}
		changed, err := rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
			return removeRejected(f, syntax, filepath.Base(path), out.Diagnostics)
		})
		if err != nil {
			return repaired, err
		}
		if !changed {
			break
		}
		repaired = true
	}
	return repaired, nil
}

// rewrite parses path, lets edit change it, and writes it back formatted if
// edit reports a change. edit gets the file both as an editable tree and as
// a syntax tree with source positions; their blocks are in the same order.
func rewrite(path string, edit func(f *hclwrite.File, syntax *hclsyntax.Body) bool) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	f, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return false, fmt.Errorf("parse %s: %w", path, diags)
	}
	syntax, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return false, fmt.Errorf("parse %s: %w", path, diags)
	}
	if !edit(f, syntax.Body.(*hclsyntax.Body)) {
		return false, nil
	}
	return true, os.WriteFile(path, hclwrite.Format(f.Bytes()), 0o644)
}

// attribute is an attribute of the generated configuration, in both trees.
type attribute struct {
	name  string
	body  *hclwrite.Body  // the body to remove it from
	block *hclsyntax.Body // the body it belongs to, to find its siblings
	rng   hcl.Range       // its source range
	zero  bool            // whether its value is 0, false, "" or empty
}

var conflictPartner = regexp.MustCompile(`conflicts with ([A-Za-z0-9_]+)`)

// removeRejected removes attributes that validation errors in filename point
// at, where removing them keeps the meaning of the configuration:
//
//   - an attribute set to its zero value (0, false, "" or empty), which
//     providers treat like unset: generated configuration lists every
//     optional argument, including ones the provider then rejects, such as
//     a 0 outside an allowed range or a false that requires other arguments;
//   - the later attribute of a conflicting pair, both of which Terraform
//     generated from the same remote value (availability_zone and
//     availability_zone_id, for example).
//
// It reports whether it removed anything.
func removeRejected(f *hclwrite.File, syntax *hclsyntax.Body, filename string, diags []tfjson.Diagnostic) bool {
	attrs := collectAttributes(syntax, f.Body(), nil)
	type rejection struct {
		attr   *attribute
		detail string
	}
	var rejected []rejection
	for _, d := range diags {
		if d.Severity != tfjson.DiagnosticSeverityError || d.Range == nil || filepath.Base(d.Range.Filename) != filename {
			continue
		}
		if a := attributeAt(attrs, d.Range.Start.Line); a != nil {
			rejected = append(rejected, rejection{a, d.Detail})
		}
	}
	// Zero values first, so a conflict with a removed attribute is resolved.
	removed := map[*attribute]bool{}
	for _, r := range rejected {
		if r.attr.zero {
			removed[r.attr] = true
		}
	}
	for _, r := range rejected {
		if removed[r.attr] {
			continue
		}
		m := conflictPartner.FindStringSubmatch(r.detail)
		if m == nil {
			continue
		}
		partner := sibling(attrs, r.attr, m[1])
		if partner != nil && !removed[partner] && partner.rng.Start.Line < r.attr.rng.Start.Line {
			removed[r.attr] = true
		}
	}
	for a := range removed {
		a.body.RemoveAttribute(a.name)
	}
	return len(removed) > 0
}

// collectAttributes lists the attributes of a body and its nested blocks.
func collectAttributes(syntax *hclsyntax.Body, body *hclwrite.Body, attrs []*attribute) []*attribute {
	for name, a := range syntax.Attributes {
		v, diags := a.Expr.Value(nil)
		attrs = append(attrs, &attribute{
			name:  name,
			body:  body,
			block: syntax,
			rng:   a.SrcRange,
			zero:  !diags.HasErrors() && isZero(v),
		})
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) {
			attrs = collectAttributes(b.Body, blocks[i].Body(), attrs)
		}
	}
	return attrs
}

// attributeAt returns the innermost attribute whose source spans line.
func attributeAt(attrs []*attribute, line int) *attribute {
	var found *attribute
	for _, a := range attrs {
		if a.rng.Start.Line <= line && line <= a.rng.End.Line &&
			(found == nil || a.rng.End.Line-a.rng.Start.Line < found.rng.End.Line-found.rng.Start.Line) {
			found = a
		}
	}
	return found
}

// sibling returns the attribute called name in the same body as a.
func sibling(attrs []*attribute, a *attribute, name string) *attribute {
	for _, s := range attrs {
		if s.block == a.block && s.name == name {
			return s
		}
	}
	return nil
}

// isZero reports whether v is 0, false, "" or empty. null is not zero here:
// Terraform writes null for values it can't show, such as sensitive ones
// ("value = null # sensitive"), and the user has to fill those in.
func isZero(v cty.Value) bool {
	switch {
	case v.IsNull(), !v.IsKnown():
		return false
	case v.Type() == cty.String:
		return v.AsString() == ""
	case v.Type() == cty.Number:
		return v.Equals(cty.Zero).True()
	case v.Type() == cty.Bool:
		return v.False()
	case v.Type().IsObjectType():
		return len(v.Type().AttributeTypes()) == 0
	case v.CanIterateElements():
		return v.LengthInt() == 0
	}
	return false
}

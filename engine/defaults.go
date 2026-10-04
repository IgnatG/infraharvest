// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// maxDefaultRounds bounds how often stripDefaults plans.
const maxDefaultRounds = 3

// defaultCandidate is an argument stripDefaults may leave out.
type defaultCandidate struct {
	address string
	path    []string // nested block types, then the argument
	null    bool     // its value is null: leaving it out is always equal
}

func (c defaultCandidate) key() string {
	return c.address + "\x00" + strings.Join(c.path, ".")
}

// stripDefaults leaves out of generated.tf the arguments Terraform writes
// although they change nothing:
//
//   - arguments set to null, which is the same as leaving them out;
//   - optional, non-computed arguments set to a constant (false, 0, "",
//     [] or {}) that is the provider's default.
//
// Terraform writes every optional argument, so a resource lists dozens of
// them. Whether a constant is the default only the plan can tell: the
// candidates are left out, and where a resource then plans differently from
// changes (see changeSignature), its arguments behind the difference are
// put back, until the plan stays the same. Computed arguments set to a
// constant stay: leaving one out never shows in the plan, so it could
// hide a real setting. It reports whether it changed generated.tf.
func stripDefaults(ctx context.Context, tf Terraform, dir string, baseline changeSummary, changes map[string]string, vars []tfexec.PlanOption) (bool, error) {
	path := filepath.Join(dir, GeneratedFileName)
	original, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	current, err := loadHCL(path)
	if err != nil {
		return false, err
	}
	// Only constants need the schema, to tell optional from computed.
	var schemas *tfjson.ProviderSchemas
	if hasZeroConstants(current.syntax) {
		if schemas, err = tf.ProvidersSchema(ctx); err != nil {
			return false, fmt.Errorf("terraform providers schema: %w", err)
		}
	}
	restore := func() error { return os.WriteFile(path, original, 0o644) }
	// What the plan can't settle keeps its constants; nulls still go, as
	// Terraform treats null as an omitted argument.
	nullsOnly := func() (bool, error) {
		if err := restore(); err != nil {
			return false, err
		}
		f, err := loadHCL(path)
		if err != nil {
			return false, err
		}
		if len(removeDefaults(f, nil, nil)) == 0 {
			return false, nil
		}
		return true, f.save()
	}
	keep := map[string]bool{}
	for round := 0; round < maxDefaultRounds; round++ {
		if err := restore(); err != nil {
			return false, err
		}
		generated, err := loadHCL(path)
		if err != nil {
			return false, err
		}
		removed := removeDefaults(generated, schemas, keep)
		if len(removed) == 0 {
			return false, nil
		}
		if err := generated.save(); err != nil {
			return false, err
		}
		p, summary, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return false, err
		}
		if len(diags) > 0 || p == nil {
			// Leaving an argument out made the configuration invalid, such
			// as one of two required alternatives: keep what the errors
			// are about, or everything.
			imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
			if err != nil {
				return false, err
			}
			edited, err := loadHCL(path)
			if err != nil {
				return false, err
			}
			assigned, unassigned := byResource(diags, edited, imports)
			if len(unassigned) > 0 || len(assigned) == 0 {
				return nullsOnly()
			}
			for _, c := range removed {
				if _, ok := assigned[c.address]; ok {
					keep[c.key()] = true
				}
			}
			continue
		}
		differs := false
		for _, rc := range p.ResourceChanges {
			if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
				continue
			}
			if !noWorse(changeSignature(rc), changes[rc.Address]) {
				differs = true
				keepChanged(removed, rc.Address, newlyChanged(changeSignature(rc), changes[rc.Address]), keep)
			}
		}
		if !differs && summary != nil && summary.Add == baseline.Add && summary.Remove == baseline.Remove && summary.Change <= baseline.Change {
			return true, nil
		}
		if !differs {
			// The totals changed without a resource to blame.
			return nullsOnly()
		}
	}
	return nullsOnly()
}

// keepChanged keeps the constant candidates of address behind changed:
// those under a newly changed top-level argument, or all of them if none
// is. Nulls never count: Terraform treats null as an omitted argument.
func keepChanged(removed []defaultCandidate, address string, changed []string, keep map[string]bool) {
	matched := false
	for _, c := range removed {
		if !c.null && c.address == address && slices.Contains(changed, c.path[0]) {
			keep[c.key()] = true
			matched = true
		}
	}
	if matched {
		return
	}
	for _, c := range removed {
		if !c.null && c.address == address {
			keep[c.key()] = true
		}
	}
}

// newlyChanged returns the attributes a change signature, now, changes
// that the one before didn't.
func newlyChanged(now, before string) []string {
	_, nowAttributes, _ := strings.Cut(now, ":")
	_, beforeAttributes, _ := strings.Cut(before, ":")
	previous := strings.Split(beforeAttributes, ",")
	var changed []string
	for _, a := range strings.Split(nowAttributes, ",") {
		if a != "" && !slices.Contains(previous, a) {
			changed = append(changed, a)
		}
	}
	return changed
}

// noWorse reports whether a resource's planned change, now, does no more
// than it did before: the same actions, and no attribute that didn't
// change before. Leaving out an argument may only remove changes, such as
// a provider-side setting the import left unset.
func noWorse(now, before string) bool {
	if now == before {
		return true
	}
	nowActions, nowAttributes, _ := strings.Cut(now, ":")
	beforeActions, beforeAttributes, _ := strings.Cut(before, ":")
	if nowActions != beforeActions && nowActions != string(tfjson.ActionNoop) {
		return false
	}
	before2 := strings.Split(beforeAttributes, ",")
	for _, a := range strings.Split(nowAttributes, ",") {
		if a != "" && !slices.Contains(before2, a) {
			return false
		}
	}
	return true
}

// removeDefaults removes the candidates not in keep from every resource,
// and returns them.
func removeDefaults(f *hclFile, schemas *tfjson.ProviderSchemas, keep map[string]bool) []defaultCandidate {
	var removed []defaultCandidate
	for _, r := range f.resources() {
		typ := resourceTypeOf(r.address)
		removed = append(removed, removeIn(r.address, typ, nil, r.syntax.Body, r.write.Body(), schemas, keep)...)
	}
	return removed
}

func removeIn(address, typ string, prefix []string, syntax *hclsyntax.Body, write *hclwrite.Body, schemas *tfjson.ProviderSchemas, keep map[string]bool) []defaultCandidate {
	var removed []defaultCandidate
	for name, attr := range syntax.Attributes {
		path := append(append([]string(nil), prefix...), name)
		c := defaultCandidate{address: address, path: path}
		switch {
		case isNullLiteral(attr.Expr):
			c.null = true
		case isZeroConstant(attr.Expr):
			s := schemaAttribute(schemas, typ, path)
			if s == nil || !s.Optional || s.Computed || s.Required {
				continue
			}
		default:
			continue
		}
		if keep[c.key()] {
			continue
		}
		write.RemoveAttribute(name)
		removed = append(removed, c)
	}
	blocks := write.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) {
			removed = append(removed, removeIn(address, typ, append(append([]string(nil), prefix...), b.Type), b.Body, blocks[i].Body(), schemas, keep)...)
		}
	}
	return removed
}

func isNullLiteral(expr hclsyntax.Expression) bool {
	lit, ok := expr.(*hclsyntax.LiteralValueExpr)
	return ok && lit.Val.IsNull()
}

// isZeroConstant reports whether expr is false, 0, "", [] or {}.
func isZeroConstant(expr hclsyntax.Expression) bool {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		v := e.Val
		if v.IsNull() || !v.IsKnown() {
			return false
		}
		switch {
		case v.Type() == cty.Bool:
			return v.False()
		case v.Type() == cty.Number:
			return v.Equals(cty.Zero).True()
		}
	case *hclsyntax.TemplateExpr:
		if len(e.Parts) == 0 {
			return true
		}
		s, ok := literalString(e)
		return ok && s == ""
	case *hclsyntax.TupleConsExpr:
		return len(e.Exprs) == 0
	case *hclsyntax.ObjectConsExpr:
		return len(e.Items) == 0
	}
	return false
}

// hasZeroConstants reports whether a body or its nested blocks set an
// argument to false, 0, "", [] or {}.
func hasZeroConstants(body *hclsyntax.Body) bool {
	for _, attr := range body.Attributes {
		if isZeroConstant(attr.Expr) {
			return true
		}
	}
	for _, b := range body.Blocks {
		if hasZeroConstants(b.Body) {
			return true
		}
	}
	return false
}

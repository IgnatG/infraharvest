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

// maxDefaultRounds bounds how often stripDefaults plans, and
// maxDefaultValidations how often it validates before each plan.
const (
	maxDefaultRounds      = 4
	maxDefaultValidations = 3
)

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
//   - optional, non-computed arguments set to a constant that is the
//     provider's default, whatever the constant: false, 0 and "" as much
//     as 3600, "/" or "Managed by Terraform";
//   - optional arguments, computed or not, set to the value defaults
//     names for their type, from the catalogue: what the provider or the
//     cloud gives an argument it isn't told, such as a bucket's STANDARD
//     storage class, by its path in nested blocks
//     (soft_delete_policy.retention_duration_seconds=604800).
//
// Terraform writes every optional argument, so a resource lists dozens of
// them. The providers' schemas don't say what their defaults are, so only
// the plan can tell a default from a setting: the candidates of every
// resource are left out at once, and where a resource then plans
// differently from changes (see changeSignature), its arguments behind
// the difference are put back, the next time all of its arguments, until
// the plan stays the same. Before each plan, validation, which needs no
// cloud, puts back what leaving out made invalid, such as one of two
// required alternatives. Other computed arguments set to a constant
// stay: leaving one out never shows in the plan, so it could hide a real
// setting. It reports whether it changed generated.tf.
func stripDefaults(ctx context.Context, tf Terraform, dir string, baseline changeSummary, changes map[string]string, vars []tfexec.PlanOption, defaults map[string][]string) (bool, error) {
	path := filepath.Join(dir, GeneratedFileName)
	original, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	current, err := loadHCL(path)
	if err != nil {
		return false, err
	}
	// Only constants need the schema, to tell optional from computed, and
	// empty blocks, which only a plan can tell from absent ones.
	var schemas *tfjson.ProviderSchemas
	if len(defaults) > 0 || hasConstants(current.syntax) || slices.ContainsFunc(current.syntax.Blocks, func(b *hclsyntax.Block) bool { return hasEmptyBlocks(b.Body) }) {
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
		if len(removeDefaults(f, nil, nil, nil)) == 0 {
			return false, nil
		}
		return true, f.save()
	}
	keep := map[string]bool{}
	// blamed counts the rounds in which each resource planned differently.
	blamed := map[string]int{}
	for round := 0; round < maxDefaultRounds; round++ {
		removed, err := removeValid(ctx, tf, dir, restore, schemas, keep, defaults)
		if err != nil {
			return false, err
		}
		if len(removed) == 0 {
			return false, restore()
		}
		p, summary, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return false, err
		}
		if len(diags) > 0 || p == nil {
			// Leaving an argument out made the configuration invalid in a
			// way validation didn't see: keep what the errors are about,
			// or everything.
			assigned, unassigned, err := assignErrors(dir, diags)
			if err != nil {
				return false, err
			}
			if len(unassigned) > 0 || len(assigned) == 0 {
				return nullsOnly()
			}
			for address := range assigned {
				keepAll(removed, address, keep)
			}
			continue
		}
		differs := false
		for _, rc := range p.ResourceChanges {
			if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
				continue
			}
			if noWorse(changeSignature(rc), changes[rc.Address]) {
				continue
			}
			differs = true
			blamed[rc.Address]++
			if blamed[rc.Address] > 1 {
				// The arguments behind the first difference weren't all:
				// keep the resource's constants rather than plan again.
				keepAll(removed, rc.Address, keep)
				continue
			}
			keepChanged(removed, rc.Address, newlyChanged(changeSignature(rc), changes[rc.Address]), keep)
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

// removeValid leaves the candidates not in keep out of generated.tf, as
// restore has it, until the configuration validates: the candidates of a
// resource that validation rejects, those its errors name or else all of
// them, are kept. After maxDefaultValidations, the plan has the last word.
// It returns the candidates it left out.
func removeValid(ctx context.Context, tf Terraform, dir string, restore func() error, schemas *tfjson.ProviderSchemas, keep map[string]bool, defaults map[string][]string) ([]defaultCandidate, error) {
	path := filepath.Join(dir, GeneratedFileName)
	for validation := 0; ; validation++ {
		if err := restore(); err != nil {
			return nil, err
		}
		generated, err := loadHCL(path)
		if err != nil {
			return nil, err
		}
		removed := removeDefaults(generated, schemas, keep, defaults)
		if len(removed) == 0 {
			return nil, nil
		}
		if err := generated.save(); err != nil {
			return nil, err
		}
		// Leaving out nulls never makes configuration invalid.
		if validation == maxDefaultValidations || !slices.ContainsFunc(removed, func(c defaultCandidate) bool { return !c.null }) {
			return removed, nil
		}
		out, err := tf.Validate(ctx)
		if err != nil {
			return nil, fmt.Errorf("terraform validate: %w", err)
		}
		if out.Valid {
			return removed, nil
		}
		assigned, _, err := assignErrors(dir, errorDiagnostics(out.Diagnostics))
		if err != nil {
			return nil, err
		}
		if len(assigned) == 0 {
			// Nothing to tell the resources apart by: let the plan say.
			return removed, nil
		}
		for address, ds := range assigned {
			keepNamed(removed, address, ds, keep)
		}
	}
}

// assignErrors assigns diags to the resources of dir they are about (see
// byResource).
func assignErrors(dir string, diags []tfjson.Diagnostic) (map[string][]tfjson.Diagnostic, []tfjson.Diagnostic, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, nil, err
	}
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, nil, err
	}
	assigned, unassigned := byResource(diags, generated, imports)
	return assigned, unassigned, nil
}

// keepNamed keeps the constant candidates of address that diags name, such
// as the alternatives of "one of `a,b` must be specified", or all of them
// if they name none.
func keepNamed(removed []defaultCandidate, address string, diags []tfjson.Diagnostic, keep map[string]bool) {
	var text strings.Builder
	for _, d := range diags {
		text.WriteString(d.Summary + " " + d.Detail + " ")
	}
	named := argumentNames(text.String())
	matched := false
	for _, c := range removed {
		if !c.null && c.address == address && (named[c.path[0]] || named[c.path[len(c.path)-1]]) {
			keep[c.key()] = true
			matched = true
		}
	}
	if !matched {
		keepAll(removed, address, keep)
	}
}

// argumentNames returns the words of text that could name an argument.
func argumentNames(text string) map[string]bool {
	names := map[string]bool{}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	}) {
		names[word] = true
	}
	return names
}

// keepAll keeps every constant candidate of address. Nulls never count:
// Terraform treats null as an omitted argument.
func keepAll(removed []defaultCandidate, address string, keep map[string]bool) {
	for _, c := range removed {
		if !c.null && c.address == address {
			keep[c.key()] = true
		}
	}
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
	if !matched {
		keepAll(removed, address, keep)
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
// and returns them. Without schemas, only nulls are candidates.
func removeDefaults(f *hclFile, schemas *tfjson.ProviderSchemas, keep map[string]bool, defaults map[string][]string) []defaultCandidate {
	var removed []defaultCandidate
	for _, r := range f.resources() {
		typ := resourceTypeOf(r.address)
		removed = append(removed, removeIn(r.address, typ, nil, r.syntax.Body, r.write.Body(), schemas, keep, defaultValues(defaults[typ]))...)
	}
	return removed
}

func removeIn(address, typ string, prefix []string, syntax *hclsyntax.Body, write *hclwrite.Body, schemas *tfjson.ProviderSchemas, keep map[string]bool, defaults map[string]string) []defaultCandidate {
	var removed []defaultCandidate
	for name, attr := range syntax.Attributes {
		path := append(append([]string(nil), prefix...), name)
		c := defaultCandidate{address: address, path: path}
		switch {
		case isNullLiteral(attr.Expr):
			c.null = true
		case isNamedDefault(defaults, path, attr.Expr):
			s := schemaAttribute(schemas, typ, path)
			if s == nil || !s.Optional || s.Required {
				continue
			}
		case isConstant(attr.Expr):
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
		if i >= len(blocks) {
			continue
		}
		blockPath := append(append([]string(nil), prefix...), b.Type)
		removed = append(removed, removeIn(address, typ, blockPath, b.Body, blocks[i].Body(), schemas, keep, defaults)...)
		// A block left with nothing set, such as ttl {}, only says it is
		// there: it goes too, where the plan says that's the same.
		c := defaultCandidate{address: address, path: blockPath}
		if schemas != nil && isEmpty(blocks[i].Body()) && !keep[c.key()] {
			write.RemoveBlock(blocks[i])
			removed = append(removed, c)
		}
	}
	return removed
}

// defaultValues parses a type's defaults, path=value entries such as
// storage_class=STANDARD or bgp.keepalive_interval=20, by path.
func defaultValues(entries []string) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		path, value, ok := strings.Cut(entry, "=")
		if ok && path != "" {
			values[path] = value
		}
	}
	return values
}

// isNamedDefault reports whether an attribute at path, set to expr, has the
// value defaults names for that path.
func isNamedDefault(defaults map[string]string, path []string, expr hclsyntax.Expression) bool {
	value, ok := defaults[strings.Join(path, ".")]
	return ok && matchesValue(expr, value, nil)
}

// isEmpty reports whether body sets nothing.
func isEmpty(body *hclwrite.Body) bool {
	return len(body.Attributes()) == 0 && len(body.Blocks()) == 0
}

// hasEmptyBlocks reports whether a body's nested blocks, or theirs, set
// nothing but null.
func hasEmptyBlocks(body *hclsyntax.Body) bool {
	for _, b := range body.Blocks {
		if nullBody(b.Body) || hasEmptyBlocks(b.Body) {
			return true
		}
	}
	return false
}

func isNullLiteral(expr hclsyntax.Expression) bool {
	lit, ok := expr.(*hclsyntax.LiteralValueExpr)
	return ok && lit.Val.IsNull()
}

// isConstant reports whether expr is a value that refers to nothing and
// calls nothing, such as 3600, "/", true, ["a"] or {}, and isn't null.
func isConstant(expr hclsyntax.Expression) bool {
	if len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(nil)
	return !diags.HasErrors() && !v.IsNull() && v.IsWhollyKnown()
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

// hasConstants reports whether a body or its nested blocks set an argument
// to a constant (see isConstant).
func hasConstants(body *hclsyntax.Body) bool {
	for _, attr := range body.Attributes {
		if isConstant(attr.Expr) {
			return true
		}
	}
	for _, b := range body.Blocks {
		if hasConstants(b.Body) {
			return true
		}
	}
	return false
}

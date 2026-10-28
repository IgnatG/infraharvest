// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// schemaHints is what a provider's code says about its arguments (see
// schemameta), for one root, and how far it goes there, by how the
// provider release the root's lock file records compares with the
// metadata's:
//
//   - the same release (exact): defaults are proven (see provenDefault),
//     values other than a default are settings (see provenSetting), and
//     conflicting arguments are settled before validation (see
//     resolveConflicts);
//   - another release of the same major version: defaults are proven,
//     which plans still correct (see stripDefaults), but nothing else;
//   - another major version, or a release the lock file doesn't record:
//     defaults only add candidates the plan checks.
//
// The zero value knows nothing.
type schemaHints struct {
	file *schemameta.File
	// exact says the root uses the release the metadata is of, and
	// defaults that it uses one of the same major version, or that.
	exact, defaults bool
	// constrained are, by type, the paths of the arguments a constraint
	// that needs arguments set names (ExactlyOneOf, AtLeastOneOf and
	// RequiredWith), on either side: leaving one out can make the
	// configuration invalid, so none is a proven default.
	constrained map[string]map[string]bool
}

// hintsLogged remembers the releases schemaHintsFor has said how far the
// metadata goes for, so that an import of many roots says it once.
var hintsLogged sync.Map

// constrainedPaths caches constrained (see schemaHints) by file.
var constrainedPaths sync.Map

// schemaHintsFor returns the hints of file for the root in dir, after
// init, by the release its lock file records, whether Terraform's
// registry or OpenTofu's wrote it (see lockedVersionOf).
func schemaHintsFor(dir string, file *schemameta.File) schemaHints {
	if file == nil {
		return schemaHints{}
	}
	h := schemaHints{file: file, constrained: constrainedOf(file)}
	used := ""
	if lock, err := os.ReadFile(filepath.Join(dir, tfrun.LockFileName)); err == nil {
		// The source without its registry: hashicorp/aws.
		source := file.Provider
		if parts := strings.Split(source, "/"); len(parts) == 3 {
			source = parts[1] + "/" + parts[2]
		}
		used = lockedVersionOf(lock, source)
	}
	h.exact = used != "" && used == file.Version
	h.defaults = h.exact || sameMajor(used, file.Version)
	var how string
	switch {
	case h.exact:
		how = "it proves defaults and settles conflicting arguments"
	case h.defaults:
		how = "it proves defaults, which plans correct where they are wrong, and settles no conflicting arguments"
	default:
		how = "it only suggests defaults, which plans check, and settles no conflicting arguments"
	}
	if used == "" {
		used = "a release the lock file doesn't record"
	}
	key := file.Provider + " " + file.Version + " " + used
	if _, logged := hintsLogged.LoadOrStore(key, true); !logged {
		log.Printf("%s: the built-in schema metadata is of %s, and the root uses %s: %s", file.Provider, file.Version, used, how)
	}
	return h
}

// sameMajor reports whether releases a and b are versions of the same
// major version.
func sameMajor(a, b string) bool {
	va, err := version.NewVersion(a)
	if err != nil {
		return false
	}
	vb, err := version.NewVersion(b)
	if err != nil {
		return false
	}
	return va.Segments()[0] == vb.Segments()[0]
}

// constrainedOf returns, by type, the paths of the arguments of file that
// an ExactlyOneOf, AtLeastOneOf or RequiredWith names or holds.
func constrainedOf(file *schemameta.File) map[string]map[string]bool {
	if cached, ok := constrainedPaths.Load(file); ok {
		return cached.(map[string]map[string]bool)
	}
	all := map[string]map[string]bool{}
	for typ, r := range file.Resources {
		paths := map[string]bool{}
		for path, a := range r.Arguments {
			if len(a.ExactlyOneOf)+len(a.AtLeastOneOf)+len(a.RequiredWith) == 0 {
				continue
			}
			paths[path] = true
			for _, list := range [][]string{a.ExactlyOneOf, a.AtLeastOneOf, a.RequiredWith} {
				for _, p := range list {
					paths[p] = true
				}
			}
		}
		if len(paths) > 0 {
			all[typ] = paths
		}
	}
	constrainedPaths.Store(file, all)
	return all
}

// argument returns the metadata of the argument at path of typ, and
// whether typ is a Plugin SDK resource, whose metadata is complete.
func (h schemaHints) argument(typ string, path []string) (*schemameta.Argument, bool) {
	r := h.file.Resource(typ)
	if r == nil {
		return nil, false
	}
	return r.Arguments[strings.Join(path, ".")], !r.Framework
}

// provenDefault reports whether expr, the value of the argument at path of
// typ, is the default the provider gives it, as metadata of a Plugin SDK
// resource of the root's major version says: the provider plans the same
// without it, so leaving it out needs no validation, and where the plan
// differs all the same, as it may for another release than the
// metadata's, the plan corrects it (see stripDefaults). Arguments a
// constraint names aren't: their absence can make the configuration
// invalid.
func (h schemaHints) provenDefault(typ string, path []string, expr hclsyntax.Expression) bool {
	if !h.defaults {
		return false
	}
	a, sdk := h.argument(typ, path)
	if a == nil || !sdk || a.Default == nil || h.constrained[typ][strings.Join(path, ".")] {
		return false
	}
	return equalsDefault(expr, a.Default)
}

// provenSetting reports whether expr, a constant, is a value other than
// the default exact metadata of a Plugin SDK resource names for the
// argument at path of typ: left out, the argument would plan as the
// default, a change, so it isn't one to try leaving out. Only the exact
// release's metadata says so: nothing would correct a wrong one but a
// default kept.
func (h schemaHints) provenSetting(typ string, path []string, expr hclsyntax.Expression) bool {
	if !h.exact || !isConstant(expr) {
		return false
	}
	a, sdk := h.argument(typ, path)
	if a == nil || !sdk || a.Default == nil || a.Suppress {
		return false
	}
	return !equalsDefault(expr, a.Default)
}

// frameworkDefault reports whether expr is the default the metadata names
// for the argument at path of typ, a Plugin Framework resource's. Such an
// argument is computed, which keeps its constants otherwise (see
// removeIn), but left out it plans as the default, so the plan can tell.
func (h schemaHints) frameworkDefault(typ string, path []string, expr hclsyntax.Expression) bool {
	a, sdk := h.argument(typ, path)
	return a != nil && !sdk && a.Default != nil && equalsDefault(expr, a.Default)
}

// equalsDefault reports whether expr is a constant equal to def, a
// metadata default: a bool, a json.Number or a string.
func equalsDefault(expr hclsyntax.Expression, def any) bool {
	if !isConstant(expr) {
		return false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() {
		return false
	}
	switch d := def.(type) {
	case bool:
		return v.Type() == cty.Bool && v.True() == d
	case string:
		return v.Type() == cty.String && v.AsString() == d
	case json.Number:
		n, err := cty.ParseNumberVal(d.String())
		return err == nil && v.Type() == cty.Number && v.Equals(n).True()
	}
	return false
}

// resolveConflicts settles, in the configuration at path, the arguments
// the metadata says can't be set together (ConflictsWith, ExactlyOneOf)
// before any validation has to say so: it hands removeRejected the
// errors validation would report, so that the same one of each pair goes
// as repair would remove. Terraform generates both of such a pair from
// the same remote value, such as a subnet's availability_zone and
// availability_zone_id. Only a resource's own arguments are settled, not
// those of its nested blocks: the Plugin SDK checks a nested argument's
// constraint, such as blk.0.x, against the first block only, so in
// another block the same pair may not conflict. Only exact metadata
// settles them: another release
// may let both be set, and then the arguments stay for validation and
// repair to judge, as without metadata. It reports whether it changed the
// file.
func resolveConflicts(path string, h schemaHints) (bool, error) {
	if h.file == nil || !h.exact {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		diags := conflictDiagnostics(syntax, h, filepath.Base(path))
		return len(diags) > 0 && removeRejected(f, syntax, filepath.Base(path), diags)
	})
}

// conflictDiagnostics returns the errors validation reports about the
// conflicting top-level arguments of syntax's resources, worded as the
// Plugin SDK words them.
func conflictDiagnostics(syntax *hclsyntax.Body, h schemaHints, filename string) []tfjson.Diagnostic {
	var diags []tfjson.Diagnostic
	for _, b := range syntax.Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 {
			continue
		}
		if r := h.file.Resource(b.Labels[0]); r != nil && !r.Framework {
			diags = append(diags, bodyConflicts(b.Body, r, filename)...)
		}
	}
	return diags
}

// bodyConflicts returns the conflict errors of body, a resource's: those
// between its own arguments, and between an argument and a nested block.
func bodyConflicts(body *hclsyntax.Body, r *schemameta.Resource, filename string) []tfjson.Diagnostic {
	set := func(name string) bool {
		if attr, ok := body.Attributes[name]; ok {
			return !isNullLiteral(attr.Expr)
		}
		for _, b := range body.Blocks {
			if b.Type == name {
				return true
			}
		}
		return false
	}
	// topLevel returns the argument a constraint names, if it is one of the
	// resource's own.
	topLevel := func(path string) (string, bool) {
		return path, !strings.Contains(path, ".")
	}
	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	var diags []tfjson.Diagnostic
	for _, name := range names {
		attr := body.Attributes[name]
		a := r.Arguments[name]
		if a == nil || isNullLiteral(attr.Expr) {
			continue
		}
		at := func(detail string) {
			diags = append(diags, tfjson.Diagnostic{
				Severity: tfjson.DiagnosticSeverityError,
				Summary:  "Conflicting configuration arguments",
				Detail:   detail,
				Range:    &tfjson.Range{Filename: filename, Start: tfjson.Pos{Line: attr.SrcRange.Start.Line}, End: tfjson.Pos{Line: attr.SrcRange.End.Line}},
			})
		}
		for _, p := range a.ConflictsWith {
			if other, ok := topLevel(p); ok && other != name && set(other) {
				at(fmt.Sprintf("%q: conflicts with %s", name, other))
			}
		}
		var members, specified []string
		for _, p := range a.ExactlyOneOf {
			if member, ok := topLevel(p); ok {
				members = append(members, member)
				if set(member) {
					specified = append(specified, member)
				}
			}
		}
		if len(specified) > 1 {
			at(fmt.Sprintf("%q: only one of `%s` can be specified, but `%s` were specified.", name, strings.Join(members, ","), strings.Join(specified, ",")))
		}
	}
	return diags
}

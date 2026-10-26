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

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
)

// schemaHints is what a provider's code says about its arguments (see
// schemameta), for one root: exact says the metadata is of the provider
// release the root's lock file records. Only exact metadata proves a
// default without a plan, or a value a setting; metadata of another
// release, or none recorded, only adds candidates the plan then checks.
// The zero value knows nothing.
type schemaHints struct {
	file  *schemameta.File
	exact bool
	// constrained are, by type, the paths of the arguments a constraint
	// that needs arguments set names (ExactlyOneOf, AtLeastOneOf and
	// RequiredWith), on either side: leaving one out can make the
	// configuration invalid, so none is a proven default.
	constrained map[string]map[string]bool
}

// hintsLogged remembers the releases schemaHintsFor has said differ, so
// that an import of many roots says it once.
var hintsLogged sync.Map

// constrainedPaths caches constrained (see schemaHints) by file.
var constrainedPaths sync.Map

// schemaHintsFor returns the hints of file for the root in dir, after
// init: exact where its lock file records the release file is of.
func schemaHintsFor(dir string, file *schemameta.File) schemaHints {
	if file == nil {
		return schemaHints{}
	}
	h := schemaHints{file: file, constrained: constrainedOf(file)}
	lock, err := os.ReadFile(filepath.Join(dir, LockFileName))
	used := ""
	if err == nil {
		used = LockedVersion(lock, file.Provider)
	}
	h.exact = used == file.Version
	if !h.exact {
		key := file.Provider + " " + file.Version + " " + used
		if _, logged := hintsLogged.LoadOrStore(key, true); !logged {
			if used == "" {
				used = "a release the lock file doesn't record"
			}
			log.Printf("%s: the built-in schema metadata is of %s, and the root uses %s: it only suggests defaults, which plans check", file.Provider, file.Version, used)
		}
	}
	return h
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
// typ, is the default the provider gives it, as exact metadata of a
// Plugin SDK resource says: the provider plans the same without it, so
// leaving it out needs no plan to tell. Arguments a constraint names
// aren't: their absence can make the configuration invalid.
func (h schemaHints) provenDefault(typ string, path []string, expr hclsyntax.Expression) bool {
	if !h.exact {
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
// default, a change, so it isn't one to try leaving out.
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
// availability_zone_id. It reports whether it changed the file.
func resolveConflicts(path string, h schemaHints) (bool, error) {
	if h.file == nil {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		diags := conflictDiagnostics(syntax, h, filepath.Base(path))
		return len(diags) > 0 && removeRejected(f, syntax, filepath.Base(path), diags)
	})
}

// conflictDiagnostics returns the errors validation reports about the
// conflicting arguments of syntax's resources, worded as the Plugin SDK
// words them.
func conflictDiagnostics(syntax *hclsyntax.Body, h schemaHints, filename string) []tfjson.Diagnostic {
	var diags []tfjson.Diagnostic
	for _, b := range syntax.Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 {
			continue
		}
		if r := h.file.Resource(b.Labels[0]); r != nil && !r.Framework {
			diags = append(diags, bodyConflicts(b.Body, r, "", filename)...)
		}
	}
	return diags
}

// bodyConflicts returns the conflict errors of body, the body of a
// resource or of one of its nested blocks at prefix, and of its blocks.
// Only constraints between arguments of the same body count: the
// generated configuration names no others the same way.
func bodyConflicts(body *hclsyntax.Body, r *schemameta.Resource, prefix, filename string) []tfjson.Diagnostic {
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
	inBody := func(path string) (string, bool) {
		name, ok := strings.CutPrefix(path, prefix)
		return name, ok && !strings.Contains(name, ".")
	}
	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	var diags []tfjson.Diagnostic
	for _, name := range names {
		attr := body.Attributes[name]
		a := r.Arguments[prefix+name]
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
			if other, ok := inBody(p); ok && other != name && set(other) {
				at(fmt.Sprintf("%q: conflicts with %s", name, other))
			}
		}
		var members, specified []string
		for _, p := range a.ExactlyOneOf {
			if member, ok := inBody(p); ok {
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
	for _, b := range body.Blocks {
		diags = append(diags, bodyConflicts(b.Body, r, prefix+b.Type+".", filename)...)
	}
	return diags
}

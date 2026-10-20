// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// CheckStandards is the verification gate's built-in standards check (G4).
const CheckStandards = "G4 standards"

// exactVersion is a registry module version pinned exactly.
var exactVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// moduleFiles are the files every generated local module has.
var moduleFiles = []string{"main.tf", "variables.tf", "outputs.tf", VersionsFileName, ReadmeFileName}

// checkStandards checks a root, and the local modules it calls, against the
// output standard: pinned Terraform, providers and modules; typed and
// described variables and outputs; no legacy interpolation; no secret
// variable with a default other than null; none of the arguments omit
// leaves out; no state files but a root's local state (see
// localStateFile); only .tf files, and under vars only a merged
// root's var files (see checkVarFiles). Each finding names its rule. The
// organisation's own modules in directories, own, are its to shape: the
// root's call of one is checked, not the module.
func checkStandards(dir string, omit map[string][]string, own []string) (Check, error) {
	check := Check{Name: CheckStandards, Passed: true}
	fail := func(rule, format string, args ...any) {
		check.Passed = false
		check.Details = append(check.Details, rule+": "+fmt.Sprintf(format, args...))
	}
	modules, err := standardsIn(dir, dir, true, omit, fail)
	if err != nil {
		return check, err
	}
	if err := checkVarFiles(dir, fail); err != nil {
		return check, err
	}
	sort.Strings(modules)
	for _, m := range slices.Compact(modules) {
		if slices.ContainsFunc(own, func(o string) bool { return sameDir(o, m) }) {
			continue
		}
		for _, name := range moduleFiles {
			if _, err := os.Stat(filepath.Join(m, name)); err != nil {
				fail("module-structure", "%s has no %s", rel(dir, m), name)
			}
		}
		if _, err := os.Stat(filepath.Join(m, "examples")); err == nil {
			fail("module-structure", "%s has an examples directory", rel(dir, m))
		}
		if _, err := standardsIn(dir, m, false, omit, fail); err != nil {
			return check, err
		}
	}
	return check, nil
}

// localStateFile reports whether name is a file Terraform's local backend
// keeps a root's state in, its backups and its lock, which the output's
// .gitignore leaves out (see GitignoreFile); its workspaces' states are
// in the directory terraform.tfstate.d.
func localStateFile(name string) bool {
	return strings.HasPrefix(name, stagedStateFile) || name == ".terraform.tfstate.lock.info"
}

// standardsIn checks the files of dir, a root or a module, naming them
// relative to base, and returns the local module directories it calls.
func standardsIn(base, dir string, root bool, omit map[string][]string, fail func(rule, format string, args ...any)) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var modules []string
	requiredVersion := false
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
			continue
		case strings.HasSuffix(name, ".tofu") || strings.HasSuffix(name, ".tf.json"):
			fail("cross-dialect-syntax", "%s: only .tf files work on both Terraform and OpenTofu", rel(base, filepath.Join(dir, name)))
			continue
		case root && localStateFile(name):
			// The state of a root applied without a backend, which the
			// output's .gitignore keeps out of version control.
			continue
		case strings.Contains(name, ".tfstate"):
			fail("committed-state-file", "%s is a state file", rel(base, filepath.Join(dir, name)))
			continue
		case !strings.HasSuffix(name, ".tf"):
			continue
		}
		f, err := loadHCL(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		where := func(rng hcl.Range) string {
			return fmt.Sprintf("%s:%d", rel(base, filepath.Join(dir, name)), rng.Start.Line)
		}
		for _, b := range f.syntax.Blocks {
			switch b.Type {
			case "terraform":
				if checkTerraformBlock(b, root, where, fail) {
					requiredVersion = true
				}
			case "variable", "output", "module":
				// The syntax allows a block without its label; validate
				// (G2) reports it, and the checks below need it.
				if len(b.Labels) != 1 {
					fail("invalid-block", "%s: %s block needs one label", where(b.DefRange()), b.Type)
					continue
				}
				switch b.Type {
				case "variable":
					checkVariable(b, where, fail)
				case "output":
					if _, ok := b.Body.Attributes["description"]; !ok {
						fail("undocumented-output", "%s: output %s has no description", where(b.DefRange()), b.Labels[0])
					}
				case "module":
					if m := checkModuleCall(dir, b, where, fail); m != "" {
						modules = append(modules, m)
					}
				}
			case "resource":
				checkOmitted(b, omit, where, fail)
			}
		}
		// The visitor reports through fail and returns no diagnostics.
		_ = hclsyntax.VisitAll(f.syntax, func(n hclsyntax.Node) hcl.Diagnostics {
			if w, ok := n.(*hclsyntax.TemplateWrapExpr); ok {
				fail("legacy-interpolation", "%s: \"${...}\" around a single expression", where(w.Range()))
			}
			return nil
		})
	}
	if !requiredVersion {
		fail("missing-required-version", "%s has no required_version", rel(base, dir))
	}
	return modules, nil
}

// checkTerraformBlock checks the terraform block: required_version, and
// each provider's source and version. Roots pin within a major release
// (and providers with ~>); modules only need lower bounds. It reports
// whether the block sets required_version.
func checkTerraformBlock(b *hclsyntax.Block, root bool, where func(hcl.Range) string, fail func(rule, format string, args ...any)) bool {
	attr, ok := b.Body.Attributes["required_version"]
	if ok && root {
		if v, ok := constantString(attr.Expr); !ok || !strings.Contains(v, "<") {
			fail("unpinned-required-version", "%s: required_version must stay within one major release", where(attr.SrcRange))
		}
	}
	for _, inner := range b.Body.Blocks {
		if inner.Type != "required_providers" {
			continue
		}
		for name, p := range inner.Body.Attributes {
			v, diags := p.Expr.Value(nil)
			if diags.HasErrors() || !v.Type().IsObjectType() || !v.Type().HasAttribute("source") {
				fail("missing-required-providers", "%s: provider %s has no source", where(p.SrcRange), name)
				continue
			}
			version := ""
			if v.Type().HasAttribute("version") && v.GetAttr("version").Type() == cty.String {
				version = v.GetAttr("version").AsString()
			}
			switch {
			case version == "":
				fail("unpinned-provider-version", "%s: provider %s has no version constraint", where(p.SrcRange), name)
			case root && !strings.HasPrefix(version, "~>"):
				fail("unpinned-provider-version", "%s: provider %s must be pinned with ~> MAJOR.MINOR, not %q", where(p.SrcRange), name, version)
			}
		}
	}
	return ok
}

// checkVariable: typed, described, and no default for a sensitive one but
// null, which persists no secret: a merged root's, for the environments
// that don't set it (see writeMergedRoot).
func checkVariable(b *hclsyntax.Block, where func(hcl.Range) string, fail func(rule, format string, args ...any)) {
	name := b.Labels[0]
	if _, ok := b.Body.Attributes["type"]; !ok {
		fail("untyped-variable", "%s: variable %s has no type", where(b.DefRange()), name)
	}
	if _, ok := b.Body.Attributes["description"]; !ok {
		fail("undocumented-variable", "%s: variable %s has no description", where(b.DefRange()), name)
	}
	if s, ok := b.Body.Attributes["sensitive"]; ok {
		if v, diags := s.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.Bool && v.True() {
			if d, ok := b.Body.Attributes["default"]; ok && !isNull(d.Expr) {
				fail("secret-persisted-to-state", "%s: sensitive variable %s has a default", where(b.DefRange()), name)
			}
		}
	}
}

// isNull reports whether expr is null.
func isNull(expr hclsyntax.Expression) bool {
	v, diags := expr.Value(nil)
	return !diags.HasErrors() && v.IsNull()
}

// varsDirName is the directory of a merged root's var files (see
// writeMergedRoot).
const varsDirName = "vars"

// checkVarFiles checks the vars directory of dir, where a merged root
// keeps the values each environment sets (see writeMergedRoot): only
// .tfvars and .hcl files, each of which parses, and none that sets a
// variable the root declares sensitive.
func checkVarFiles(dir string, fail func(rule, format string, args ...any)) error {
	vars := filepath.Join(dir, varsDirName)
	if info, err := os.Stat(vars); err != nil || !info.IsDir() {
		return nil
	}
	sensitive, err := sensitiveVariables(dir)
	if err != nil {
		return err
	}
	return filepath.WalkDir(vars, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := rel(dir, path)
		if !strings.HasSuffix(name, ".tfvars") && !strings.HasSuffix(name, ".hcl") {
			fail("unexpected-var-file", "%s: only .tfvars and .hcl files belong under %s", name, varsDirName)
			return nil
		}
		f, err := loadHCL(path)
		if err != nil {
			fail("invalid-var-file", "%s doesn't parse", name)
			return nil
		}
		if !strings.HasSuffix(name, ".tfvars") {
			return nil
		}
		for _, v := range slices.Sorted(maps.Keys(f.syntax.Attributes)) {
			if sensitive[v] {
				fail("secret-in-var-file", "%s:%d: sets %s, a sensitive variable, which belongs outside the files", name, f.syntax.Attributes[v].SrcRange.Start.Line, v)
			}
		}
		return nil
	})
}

// sensitiveVariables returns the variables the root in dir declares
// sensitive.
func sensitiveVariables(dir string) (map[string]bool, error) {
	files, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	sensitive := map[string]bool{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "variable" || len(b.Labels) != 1 {
				continue
			}
			if s, ok := b.Body.Attributes["sensitive"]; ok {
				if v, diags := s.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.Bool && v.True() {
					sensitive[b.Labels[0]] = true
				}
			}
		}
	}
	return sensitive, nil
}

// checkModuleCall checks a module call's version pin, and returns the
// directory of a local module, or "".
func checkModuleCall(dir string, b *hclsyntax.Block, where func(hcl.Range) string, fail func(rule, format string, args ...any)) string {
	attr, ok := b.Body.Attributes["source"]
	if !ok {
		return ""
	}
	source, ok := constantString(attr.Expr)
	if !ok {
		return ""
	}
	switch {
	case strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../"):
		return filepath.Join(dir, filepath.FromSlash(source))
	case strings.HasPrefix(source, "git::") || strings.HasPrefix(source, "github.com/"):
		if !strings.Contains(source, "?ref=") {
			fail("unpinned-module-version", "%s: module %s from git has no ?ref=", where(attr.SrcRange), b.Labels[0])
		}
	default:
		version, ok := b.Body.Attributes["version"]
		v := ""
		if ok {
			v, _ = constantString(version.Expr)
		}
		if !exactVersion.MatchString(v) {
			fail("unpinned-module-version", "%s: module %s must pin an exact version", where(b.DefRange()), b.Labels[0])
		}
	}
	return ""
}

// sameDir reports whether two paths are the same directory.
func sameDir(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// checkOmitted fails a resource that sets an argument its provider leaves
// out of generated configuration, such as an S3 bucket's deprecated
// inline arguments, or sets one to the value it is left out for, where
// the entry's conditions hold.
func checkOmitted(b *hclsyntax.Block, omit map[string][]string, where func(hcl.Range) string, fail func(rule, format string, args ...any)) {
	if len(b.Labels) != 2 {
		return
	}
	for _, o := range omissions(omit, b.Labels[0], "") {
		for _, r := range o.find(b.Body) {
			if r.block >= 0 {
				fail("deprecated-argument", "%s: %s.%s has a %s block", where(r.rng), b.Labels[0], b.Labels[1], r.path())
			} else {
				fail("deprecated-argument", "%s: %s.%s sets %s", where(r.rng), b.Labels[0], b.Labels[1], r.path())
			}
		}
	}
}

func constantString(expr hclsyntax.Expression) (string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// rel names path relative to dir, with forward slashes; "." for dir.
func rel(dir, path string) string {
	r, err := filepath.Rel(dir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(r)
}

// ownModuleDirs are the directories of the organisation's own modules the
// adapters call (see adapters.Generic).
func ownModuleDirs(list []adapters.Adapter) []string {
	var dirs []string
	for _, a := range list {
		if filepath.IsAbs(a.Source) {
			dirs = append(dirs, a.Source)
		}
	}
	return dirs
}

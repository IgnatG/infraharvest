// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
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
// variable with a default; none of the arguments omit leaves out; no state
// files; only .tf files. Each finding names its rule.
func checkStandards(dir string, omit map[string][]string) (Check, error) {
	check := Check{Name: CheckStandards, Passed: true}
	fail := func(rule, format string, args ...any) {
		check.Passed = false
		check.Details = append(check.Details, rule+": "+fmt.Sprintf(format, args...))
	}
	modules, err := standardsIn(dir, dir, true, omit, fail)
	if err != nil {
		return check, err
	}
	sort.Strings(modules)
	for _, m := range slices.Compact(modules) {
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

// checkVariable: typed, described, and no default for a sensitive one.
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
			if _, ok := b.Body.Attributes["default"]; ok {
				fail("secret-persisted-to-state", "%s: sensitive variable %s has a default", where(b.DefRange()), name)
			}
		}
	}
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

// checkOmitted fails a resource that sets an argument its provider leaves
// out of generated configuration, such as an S3 bucket's deprecated
// inline arguments.
func checkOmitted(b *hclsyntax.Block, omit map[string][]string, where func(hcl.Range) string, fail func(rule, format string, args ...any)) {
	if len(b.Labels) != 2 {
		return
	}
	for _, name := range append(append([]string(nil), omit["*"]...), omit[b.Labels[0]]...) {
		if attr, ok := b.Body.Attributes[name]; ok {
			fail("deprecated-argument", "%s: %s.%s sets %s", where(attr.SrcRange), b.Labels[0], b.Labels[1], name)
		}
		for _, inner := range b.Body.Blocks {
			if inner.Type == name {
				fail("deprecated-argument", "%s: %s.%s has a %s block", where(inner.DefRange()), b.Labels[0], b.Labels[1], name)
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

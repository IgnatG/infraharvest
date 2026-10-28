// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// VarsDir is the directory of a merged root's var files: the root
// several environments share keeps what differs between them under it
// (see CommonVarsPath). The secrets and standards checks read those files
// too.
const VarsDir = "vars"

// IsMergedVarFile reports whether rel, a file of a root by its path in it
// with forward slashes, is one of the var files a merged root keeps under
// VarsDir, such as vars/prod/terraform.tfvars or vars/prod/backend.hcl,
// whose arguments set variables or backend settings: a .tfvars or .hcl
// file, the only kinds that belong there.
func IsMergedVarFile(rel string) bool {
	return strings.HasPrefix(rel, VarsDir+"/") && (strings.HasSuffix(rel, ".tfvars") || strings.HasSuffix(rel, ".hcl"))
}

// The layout of a merged root, the one root several environments share:
// its code at the top, and under VarsDir what differs between them, the
// values every environment shares in vars/common.tfvars, and in
// vars/<env> each environment's values, import IDs and backend settings.
// A root without a backend keeps each environment's state in the
// workspace named after it. Everything that writes, plans or reads such a
// root back takes its paths from here, so that they agree.
const (
	CommonVarsPath        = VarsDir + "/common.tfvars"
	EnvVarsFileName       = "terraform.tfvars"
	ImportVarsFileName    = "imports.tfvars"
	BackendConfigFileName = "backend.hcl"
)

// The variables of a merged root that imports.tf reads the import IDs of
// an environment's resources from, which vars/<env>/imports.tfvars sets:
// by ID, and by identity.
const (
	ImportIDsVariableName        = "import_ids"
	ImportIdentitiesVariableName = "import_identities"
)

// EnvVarsPath returns the var file of environment env's own values, by
// path in the root with forward slashes, as drift's edits and reports
// name it: vars/<env>/terraform.tfvars.
func EnvVarsPath(env string) string {
	return VarsDir + "/" + env + "/" + EnvVarsFileName
}

// ImportVarsPath returns the var file of the import IDs of environment
// env's resources, which imports.tf reads, by path in the root with
// forward slashes: vars/<env>/imports.tfvars.
func ImportVarsPath(env string) string {
	return VarsDir + "/" + env + "/" + ImportVarsFileName
}

// BackendConfigPath returns environment env's backend settings, which
// terraform init -backend-config reads, by path in the root with forward
// slashes: vars/<env>/backend.hcl.
func BackendConfigPath(env string) string {
	return VarsDir + "/" + env + "/" + BackendConfigFileName
}

// IsBackendConfig reports whether name, a path in the root with forward
// slashes, is an environment's backend settings (see BackendConfigPath),
// whether or not the environment still has values of its own.
func IsBackendConfig(name string) bool {
	env, ok := strings.CutPrefix(name, VarsDir+"/")
	if !ok {
		return false
	}
	env, ok = strings.CutSuffix(env, "/"+BackendConfigFileName)
	return ok && env != "" && !strings.Contains(env, "/")
}

// VarFiles returns the var files environment env plans the merged root
// with, by path in the root with forward slashes, in the order Terraform
// reads them, a later one's values over an earlier one's:
// vars/common.tfvars, vars/<env>/terraform.tfvars and, with imports,
// vars/<env>/imports.tfvars. That one only sets the import IDs imports.tf
// reads, so it counts until both are deleted after the first apply, and
// never for a root that imports nothing, as one whose resources all went
// to other roots (see envmerge.VacateMerged).
func VarFiles(env string, imports bool) []string {
	files := []string{CommonVarsPath, EnvVarsPath(env)}
	if imports {
		files = append(files, ImportVarsPath(env))
	}
	return files
}

// ExistingVarFiles returns the var files environment env plans the root
// environments share in dir with, by path in the root with forward
// slashes: those of VarFiles, imports.tfvars among them, that are there,
// as imports.tfvars isn't after the first apply.
func ExistingVarFiles(dir, env string) []string {
	var names []string
	for _, name := range VarFiles(env, true) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			names = append(names, name)
		}
	}
	return names
}

// MergedVars is the var files of a merged root that set its variables,
// vars/common.tfvars and each environment's vars/<env>/terraform.tfvars,
// parsed, by path in the root with forward slashes; files by any other
// path are ignored. It says what an environment's variables are set to,
// its own file's values over common's, as Terraform reads them (see
// VarFiles), and makes the edits that set one for one environment alone,
// which is all drift edits in a root environments share. The files are
// those on disk (see LoadMergedVars), or those of the contents drift
// edits, as the edits before have left them.
type MergedVars map[string]*File

// LoadMergedVars reads vars/common.tfvars of the merged root in dir and
// the own var files of envs; those that aren't there are left out.
func LoadMergedVars(dir string, envs []string) (MergedVars, error) {
	vars := MergedVars{}
	names := []string{CommonVarsPath}
	for _, env := range envs {
		names = append(names, EnvVarsPath(env))
	}
	for _, name := range names {
		if _, ok := vars[name]; ok {
			continue
		}
		f, err := ReadVarFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		if f != nil {
			vars[name] = f
		}
	}
	return vars, nil
}

// environments returns the environments that have var files of their
// own, sorted.
func (v MergedVars) environments() []string {
	var envs []string
	for name := range v {
		rest, ok := strings.CutPrefix(name, VarsDir+"/")
		if !ok {
			continue
		}
		if env, ok := strings.CutSuffix(rest, "/"+EnvVarsFileName); ok && env != "" && !strings.Contains(env, "/") {
			envs = append(envs, env)
		}
	}
	slices.Sort(envs)
	return envs
}

// Own returns environment env's own var file, or nil where it has none.
func (v MergedVars) Own(env string) *File {
	return v[EnvVarsPath(env)]
}

// VarValue is the argument of a var file that sets a variable.
type VarValue struct {
	Path string // by path in the root with forward slashes
	File *File
	Attr *hclsyntax.Attribute
}

// Value returns the argument that sets variable name for environment
// env: its own var file's, or else vars/common.tfvars'; false where
// neither sets it, as where the variable's default holds the value. A
// map or object one file sets replaces the other's whole, as in
// Terraform: their keys aren't merged.
func (v MergedVars) Value(env, name string) (VarValue, bool) {
	for _, file := range []string{EnvVarsPath(env), CommonVarsPath} {
		if f := v[file]; f != nil {
			if attr, ok := f.Syntax.Attributes[name]; ok {
				return VarValue{Path: file, File: f, Attr: attr}, true
			}
		}
	}
	return VarValue{}, false
}

// Values returns the values environment env's var files set its
// variables to, by name, of those that are literals: those of
// vars/common.tfvars, and over them those of its own.
func (v MergedVars) Values(env string) map[string]cty.Value {
	values := map[string]cty.Value{}
	for _, file := range []string{CommonVarsPath, EnvVarsPath(env)} {
		if f := v[file]; f != nil {
			maps.Copy(values, f.Values())
		}
	}
	return values
}

// Set returns the edit that sets variable name to text, an expression,
// for environment env alone: in its own var file, replacing the value
// there, or else adding name = text at its end, which goes over the value
// vars/common.tfvars gives the others. False where env has no var file
// of its own.
func (v MergedVars) Set(env, name string, text []byte) (Edit, bool) {
	file := EnvVarsPath(env)
	f := v[file]
	if f == nil {
		return Edit{}, false
	}
	if attr, ok := f.Syntax.Attributes[name]; ok {
		rng := attr.Expr.Range()
		return Edit{File: file, Start: rng.Start.Byte, End: rng.End.Byte, Text: text}, true
	}
	return AppendEdit(f, file, slices.Concat([]byte(name+" = "), text, []byte("\n")), false), true
}

// OwnFlags returns the bool variables of defaults, each declared one's
// default, null for one without, that environment env alone sets true,
// such as the deploy_ variables of the features, and residue, only it
// has: as its var files set them, over their defaults, true, and every
// other environment's false. A variable whose
// value can't be told for an environment isn't one.
func (v MergedVars) OwnFlags(env string, defaults map[string]cty.Value) map[string]bool {
	values := func(e string) map[string]cty.Value {
		set := v.Values(e)
		out := map[string]cty.Value{}
		for name, def := range defaults {
			out[name] = def
			if value, ok := set[name]; ok {
				out[name] = value
			}
		}
		return out
	}
	is := func(value cty.Value, want bool) bool {
		return value.IsKnown() && !value.IsNull() && value.Type().Equals(cty.Bool) && value.True() == want
	}
	flags := map[string]bool{}
	for name, value := range values(env) {
		if is(value, true) {
			flags[name] = true
		}
	}
	for _, other := range v.environments() {
		if other == env {
			continue
		}
		theirs := values(other)
		for name := range flags {
			if !is(theirs[name], false) {
				delete(flags, name)
			}
		}
	}
	return flags
}

// BackendConfigFile returns the absolute path of environment env's
// backend settings in the root environments share in dir, or why there
// are none.
func BackendConfigFile(dir, env string) (string, error) {
	path, err := filepath.Abs(filepath.Join(dir, filepath.FromSlash(BackendConfigPath(env))))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

// MergedEnvironments returns the environments of the root in dir if
// environments share it: the directories under its
// vars directory that hold a terraform.tfvars, sorted; none for another
// root.
func MergedEnvironments(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, VarsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(EnvVarsPath(e.Name())))); err == nil {
			envs = append(envs, e.Name())
		}
	}
	slices.Sort(envs)
	return envs, nil
}

// MergedImportIDs returns the import IDs vars/<env>/imports.tfvars of the
// root environments share in dir gives env's resources, by their
// addresses in the root, with, for those it imports by identity, the
// identity's ID (see tftypes.IdentityID); none once it is deleted, after
// the first apply.
func MergedImportIDs(dir, env string) (map[string]string, error) {
	path := filepath.Join(dir, filepath.FromSlash(ImportVarsPath(env)))
	f, err := ReadVarFile(path)
	if f == nil {
		return nil, err
	}
	var ids map[string]string
	if attr, ok := f.Syntax.Attributes[ImportIDsVariableName]; ok {
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() || (!v.Type().IsObjectType() && !v.Type().IsMapType()) {
			return nil, fmt.Errorf("%s: %s isn't a map of literal IDs", path, ImportIDsVariableName)
		}
		ids = map[string]string{}
		for it := v.ElementIterator(); it.Next(); {
			k, id := it.Element()
			if id.IsNull() || !id.Type().Equals(cty.String) {
				return nil, fmt.Errorf("%s: the ID of %s isn't a string", path, k.AsString())
			}
			ids[k.AsString()] = id.AsString()
		}
	}
	if attr, ok := f.Syntax.Attributes[ImportIdentitiesVariableName]; ok {
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() || (!v.Type().IsObjectType() && !v.Type().IsMapType()) {
			return nil, fmt.Errorf("%s: %s isn't a map of literal identities", path, ImportIdentitiesVariableName)
		}
		if ids == nil {
			ids = map[string]string{}
		}
		for it := v.ElementIterator(); it.Next(); {
			k, identity := it.Element()
			values, ok := LiteralIdentity(identity)
			if !ok {
				return nil, fmt.Errorf("%s: the identity of %s isn't an object of literal values", path, k.AsString())
			}
			ids[k.AsString()] = tftypes.IdentityID(values)
		}
	}
	return ids, nil
}

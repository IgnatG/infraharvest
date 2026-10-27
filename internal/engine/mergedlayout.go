// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

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
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// The layout of a merged root (see mergedRootFiles), the one root several
// environments share: its code at the top, and under varsDirName what
// differs between them, the values every environment shares in
// vars/common.tfvars, and in vars/<env> each environment's values, import
// IDs and backend settings. A root without a backend keeps each
// environment's state in the workspace named after it. Everything that
// writes, plans or reads such a root back takes its paths from here, so
// that they agree.
const (
	// varsDirName is the directory of a merged root's var files.
	varsDirName = "vars"
	// workspaceStateDir holds the local state of each workspace but the
	// default one, in terraform.tfstate.d/<workspace>/terraform.tfstate.
	workspaceStateDir     = "terraform.tfstate.d"
	commonVarsPath        = varsDirName + "/common.tfvars"
	envVarsFileName       = "terraform.tfvars"
	importVarsFileName    = "imports.tfvars"
	backendConfigFileName = "backend.hcl"
	// featureFlagPrefix starts the name of the bool variable that says
	// whether an environment has a feature, the resources only some
	// environments have (see features).
	featureFlagPrefix = "deploy_"
)

// envVarsPath returns the var file of environment env's own values, by
// path in the root with forward slashes, as drift's edits and reports
// name it: vars/<env>/terraform.tfvars.
func envVarsPath(env string) string {
	return varsDirName + "/" + env + "/" + envVarsFileName
}

// importVarsPath returns the var file of the import IDs of environment
// env's resources, which imports.tf reads, by path in the root with
// forward slashes: vars/<env>/imports.tfvars.
func importVarsPath(env string) string {
	return varsDirName + "/" + env + "/" + importVarsFileName
}

// backendConfigPath returns environment env's backend settings, which
// terraform init -backend-config reads, by path in the root with forward
// slashes: vars/<env>/backend.hcl.
func backendConfigPath(env string) string {
	return varsDirName + "/" + env + "/" + backendConfigFileName
}

// isBackendConfig reports whether name, a path in the root with forward
// slashes, is an environment's backend settings (see backendConfigPath),
// whether or not the environment still has values of its own.
func isBackendConfig(name string) bool {
	env, ok := strings.CutPrefix(name, varsDirName+"/")
	if !ok {
		return false
	}
	env, ok = strings.CutSuffix(env, "/"+backendConfigFileName)
	return ok && env != "" && !strings.Contains(env, "/")
}

// isMergedVarFile reports whether rel, a file of a root by its path in it
// with forward slashes, is one of the var files a merged root keeps under
// vars, such as vars/prod/terraform.tfvars or vars/prod/backend.hcl,
// whose arguments set variables or backend settings: a .tfvars or .hcl
// file, the only kinds that belong there (see checkVarFiles).
func isMergedVarFile(rel string) bool {
	return strings.HasPrefix(rel, varsDirName+"/") && (strings.HasSuffix(rel, ".tfvars") || strings.HasSuffix(rel, ".hcl"))
}

// workspaceStateFile returns where the merged root in dir, without a
// backend, keeps environment env's state: in the workspace named after
// it, terraform.tfstate.d/<env>/terraform.tfstate.
func workspaceStateFile(dir, env string) string {
	return filepath.Join(dir, workspaceStateDir, env, tfrun.LocalStateFileName)
}

// featureFlagName returns the name of the variable that says whether an
// environment has the feature named feature: deploy_<feature>.
func featureFlagName(feature string) string {
	return featureFlagPrefix + feature
}

// isFeatureFlag reports whether the variable named name says whether an
// environment has a feature (see featureFlagName).
func isFeatureFlag(name string) bool {
	return strings.HasPrefix(name, featureFlagPrefix)
}

// varFiles returns the var files environment env plans the merged root
// with, by path in the root with forward slashes, in the order Terraform
// reads them, a later one's values over an earlier one's:
// vars/common.tfvars, vars/<env>/terraform.tfvars and, with imports,
// vars/<env>/imports.tfvars. That one only sets the import IDs imports.tf
// reads, so it counts until both are deleted after the first apply, and
// never for a root that imports nothing, as one whose resources all went
// to other roots (see VacateMerged).
func varFiles(env string, imports bool) []string {
	files := []string{commonVarsPath, envVarsPath(env)}
	if imports {
		files = append(files, importVarsPath(env))
	}
	return files
}

// varFileArgs returns the -var-file arguments of the var files of
// environment env (see varFiles), as the commands a README gives write
// them.
func varFileArgs(env string, imports bool) string {
	var args []string
	for _, name := range varFiles(env, imports) {
		args = append(args, "-var-file="+name)
	}
	return strings.Join(args, " ")
}

// existingVarFiles returns the var files environment env plans the root
// environments share in dir with, by path in the root with forward
// slashes: those of varFiles, imports.tfvars among them, that are there,
// as imports.tfvars isn't after the first apply.
func existingVarFiles(dir, env string) []string {
	var names []string
	for _, name := range varFiles(env, true) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			names = append(names, name)
		}
	}
	return names
}

// mergedVarFiles returns the var files environment env plans the root
// environments share in dir with (see existingVarFiles), as -var-file
// options and as absolute paths.
func mergedVarFiles(dir, env string) ([]tfexec.PlanOption, []string, error) {
	return varFileOptions(dir, existingVarFiles(dir, env))
}

// varFileOptions returns names, var files of the root in dir by path in
// it with forward slashes, as -var-file options and as absolute paths.
// Terraform fails the plan on one that isn't there.
func varFileOptions(dir string, names []string) ([]tfexec.PlanOption, []string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	var vars []tfexec.PlanOption
	var paths []string
	for _, name := range names {
		path := filepath.Join(abs, filepath.FromSlash(name))
		vars = append(vars, tfexec.VarFile(path))
		paths = append(paths, path)
	}
	return vars, paths, nil
}

// mergedValues returns the values the var files of environment env of the
// root environments share in dir set its variables to, by name, of those
// that are literals (see mergedVars.values). The import IDs aren't among
// them.
func mergedValues(dir, env string) (map[string]cty.Value, error) {
	vars, err := loadMergedVars(dir, []string{env})
	if err != nil {
		return nil, err
	}
	return vars.values(env), nil
}

// mergedVars is the var files of a merged root that set its variables,
// vars/common.tfvars and each environment's vars/<env>/terraform.tfvars,
// parsed, by path in the root with forward slashes; files by any other
// path are ignored. It says what an environment's variables are set to,
// its own file's values over common's, as Terraform reads them (see
// varFiles), and makes the edits that set one for one environment alone,
// which is all drift edits in a root environments share. The files are
// those on disk (see loadMergedVars), or those of the contents drift
// edits (see driftContents), as the edits before have left them.
type mergedVars map[string]*hclfile.File

// loadMergedVars reads vars/common.tfvars of the merged root in dir and
// the own var files of envs; those that aren't there are left out.
func loadMergedVars(dir string, envs []string) (mergedVars, error) {
	vars := mergedVars{}
	names := []string{commonVarsPath}
	for _, env := range envs {
		names = append(names, envVarsPath(env))
	}
	for _, name := range names {
		if _, ok := vars[name]; ok {
			continue
		}
		f, err := hclfile.ReadVarFile(filepath.Join(dir, filepath.FromSlash(name)))
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
func (v mergedVars) environments() []string {
	var envs []string
	for name := range v {
		rest, ok := strings.CutPrefix(name, varsDirName+"/")
		if !ok {
			continue
		}
		if env, ok := strings.CutSuffix(rest, "/"+envVarsFileName); ok && env != "" && !strings.Contains(env, "/") {
			envs = append(envs, env)
		}
	}
	slices.Sort(envs)
	return envs
}

// own returns environment env's own var file, or nil where it has none.
func (v mergedVars) own(env string) *hclfile.File {
	return v[envVarsPath(env)]
}

// varValue is the argument of a var file that sets a variable.
type varValue struct {
	file string // by path in the root with forward slashes
	f    *hclfile.File
	attr *hclsyntax.Attribute
}

// value returns the argument that sets variable name for environment
// env: its own var file's, or else vars/common.tfvars'; false where
// neither sets it, as where the variable's default holds the value. A
// map or object one file sets replaces the other's whole, as in
// Terraform: their keys aren't merged.
func (v mergedVars) value(env, name string) (varValue, bool) {
	for _, file := range []string{envVarsPath(env), commonVarsPath} {
		if f := v[file]; f != nil {
			if attr, ok := f.Syntax.Attributes[name]; ok {
				return varValue{file: file, f: f, attr: attr}, true
			}
		}
	}
	return varValue{}, false
}

// values returns the values environment env's var files set its
// variables to, by name, of those that are literals: those of
// vars/common.tfvars, and over them those of its own.
func (v mergedVars) values(env string) map[string]cty.Value {
	values := map[string]cty.Value{}
	for _, file := range []string{commonVarsPath, envVarsPath(env)} {
		if f := v[file]; f != nil {
			maps.Copy(values, f.Values())
		}
	}
	return values
}

// set returns the edit that sets variable name to text, an expression,
// for environment env alone: in its own var file, replacing the value
// there, or else adding name = text at its end, which goes over the value
// vars/common.tfvars gives the others. False where env has no var file
// of its own.
func (v mergedVars) set(env, name string, text []byte) (hclfile.Edit, bool) {
	file := envVarsPath(env)
	f := v[file]
	if f == nil {
		return hclfile.Edit{}, false
	}
	if attr, ok := f.Syntax.Attributes[name]; ok {
		rng := attr.Expr.Range()
		return hclfile.Edit{File: file, Start: rng.Start.Byte, End: rng.End.Byte, Text: text}, true
	}
	return hclfile.AppendEdit(f, file, slices.Concat([]byte(name+" = "), text, []byte("\n")), false), true
}

// ownFlags returns the bool variables of defaults, each declared one's
// default, null for one without, that environment env alone sets true,
// such as the deploy_ variables of the features, and residue, only it
// has (see featureFlagName): as its var files set them, over their
// defaults, true, and every other environment's false. A variable whose
// value can't be told for an environment isn't one.
func (v mergedVars) ownFlags(env string, defaults map[string]cty.Value) map[string]bool {
	values := func(e string) map[string]cty.Value {
		set := v.values(e)
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

// backendConfigFile returns the absolute path of environment env's
// backend settings in the root environments share in dir, or why there
// are none.
func backendConfigFile(dir, env string) (string, error) {
	path, err := filepath.Abs(filepath.Join(dir, filepath.FromSlash(backendConfigPath(env))))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

// MergedEnvironments returns the environments of the root in dir if
// environments share it (see mergedRootFiles): the directories under its
// vars directory that hold a terraform.tfvars, sorted; none for another
// root.
func MergedEnvironments(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, varsDirName))
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
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(envVarsPath(e.Name())))); err == nil {
			envs = append(envs, e.Name())
		}
	}
	slices.Sort(envs)
	return envs, nil
}

// MergedImportIDs returns the import IDs vars/<env>/imports.tfvars of the
// root environments share in dir gives env's resources, by their
// addresses in the root; none once it is deleted, after the first apply.
func MergedImportIDs(dir, env string) (map[string]string, error) {
	path := filepath.Join(dir, filepath.FromSlash(importVarsPath(env)))
	f, err := hclfile.ReadVarFile(path)
	if f == nil {
		return nil, err
	}
	attr, ok := f.Syntax.Attributes[importIDsVariableName]
	if !ok {
		return nil, nil
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() || (!v.Type().IsObjectType() && !v.Type().IsMapType()) {
		return nil, fmt.Errorf("%s: %s isn't a map of literal IDs", path, importIDsVariableName)
	}
	ids := map[string]string{}
	for it := v.ElementIterator(); it.Next(); {
		k, id := it.Element()
		if id.IsNull() || !id.Type().Equals(cty.String) {
			return nil, fmt.Errorf("%s: the ID of %s isn't a string", path, k.AsString())
		}
		ids[k.AsString()] = id.AsString()
	}
	return ids, nil
}

// MergedStateLocations returns where environment env keeps the state of
// the root environments share in dir, as --managed-state names state
// (see managed.Load): the state its vars/<env>/backend.hcl names, as
// s3://<bucket>/<key>, gs://<bucket>/<prefix>/default.tfstate or
// https://<account>.blob.core.windows.net/<container>/<key>; and, as a
// root without a backend keeps each environment's state in a workspace
// named after it, terraform.tfstate.d/<env>/terraform.tfstate in dir.
func MergedStateLocations(dir, env string) ([]string, error) {
	locations := []string{workspaceStateFile(dir, env)}
	path := filepath.Join(dir, filepath.FromSlash(backendConfigPath(env)))
	f, err := hclfile.ReadVarFile(path)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return locations, nil
	}
	settings := map[string]string{}
	for name, v := range f.Values() {
		if v.Type() == cty.String && v.IsKnown() && !v.IsNull() {
			settings[name] = v.AsString()
		}
	}
	bucket, key, prefix := settings["bucket"], settings["key"], settings["prefix"]
	switch account, container := settings["storage_account_name"], settings["container_name"]; {
	case account != "" && container != "" && key != "":
		locations = append(locations, "https://"+account+".blob.core.windows.net/"+container+"/"+key)
	case bucket != "" && key != "":
		locations = append(locations, "s3://"+bucket+"/"+key)
	case bucket != "" && prefix != "":
		locations = append(locations, "gs://"+bucket+"/"+strings.TrimSuffix(prefix, "/")+"/default.tfstate")
	}
	return locations, nil
}

// MergedRootFiles returns the files of the root environments share in
// dir that a merge writes (see mergedRootFiles), by path in dir with
// forward slashes, sorted: its .tf files, README.md, .gitignore, and the
// var files and backend settings under vars/.
func MergedRootFiles(dir string) ([]string, error) {
	envs, err := MergedEnvironments(dir)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range paths {
		files = append(files, filepath.Base(path))
	}
	candidates := []string{ReadmeFileName, ".gitignore", commonVarsPath}
	for _, env := range envs {
		candidates = append(candidates, envVarsPath(env), importVarsPath(env), backendConfigPath(env))
	}
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return files, nil
}

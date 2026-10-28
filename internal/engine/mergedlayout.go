// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// featureFlagPrefix starts the name of the bool variable that says
// whether an environment has a feature, the resources only some
// environments have (see features).
const featureFlagPrefix = "deploy_"

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

// varFileArgs returns the -var-file arguments of the var files of
// environment env (see hclfile.VarFiles), as the commands a README gives
// write them.
func varFileArgs(env string, imports bool) string {
	var args []string
	for _, name := range hclfile.VarFiles(env, imports) {
		args = append(args, "-var-file="+name)
	}
	return strings.Join(args, " ")
}

// mergedValues returns the values the var files of environment env of the
// root environments share in dir set its variables to, by name, of those
// that are literals (see hclfile.MergedVars.Values). The import IDs aren't
// among them.
func mergedValues(dir, env string) (map[string]cty.Value, error) {
	vars, err := hclfile.LoadMergedVars(dir, []string{env})
	if err != nil {
		return nil, err
	}
	return vars.Values(env), nil
}

// MergedStateLocations returns where environment env keeps the state of
// the root environments share in dir, as --managed-state names state
// (see managed.Load): the state its vars/<env>/backend.hcl names, as
// s3://<bucket>/<key>, gs://<bucket>/<prefix>/default.tfstate or
// https://<account>.blob.core.windows.net/<container>/<key>; and, as a
// root without a backend keeps each environment's state in a workspace
// named after it, terraform.tfstate.d/<env>/terraform.tfstate in dir.
func MergedStateLocations(dir, env string) ([]string, error) {
	locations := []string{tfrun.WorkspaceStateFile(dir, env)}
	path := filepath.Join(dir, filepath.FromSlash(hclfile.BackendConfigPath(env)))
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
	envs, err := hclfile.MergedEnvironments(dir)
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
	candidates := []string{ReadmeFileName, ".gitignore", hclfile.CommonVarsPath}
	for _, env := range envs {
		candidates = append(candidates, hclfile.EnvVarsPath(env), hclfile.ImportVarsPath(env), hclfile.BackendConfigPath(env))
	}
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return files, nil
}

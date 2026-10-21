// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"
)

// MergedEnvironments returns the environments of the root in dir if
// environments share it (see writeMergedRoot): the directories under its
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
		if _, err := os.Stat(filepath.Join(dir, varsDirName, e.Name(), envVarsFileName)); err == nil {
			envs = append(envs, e.Name())
		}
	}
	sort.Strings(envs)
	return envs, nil
}

// MergedImportIDs returns the import IDs vars/<env>/imports.tfvars of the
// root environments share in dir gives env's resources, by their
// addresses in the root; none once it is deleted, after the first apply.
func MergedImportIDs(dir, env string) (map[string]string, error) {
	path := filepath.Join(dir, varsDirName, env, importVarsFileName)
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %w", path, diags)
	}
	attr, ok := f.Body.(*hclsyntax.Body).Attributes[importIDsVariableName]
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
	locations := []string{filepath.Join(dir, "terraform.tfstate.d", env, "terraform.tfstate")}
	path := filepath.Join(dir, varsDirName, env, backendConfigFileName)
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return locations, nil
	}
	if err != nil {
		return nil, err
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %w", path, diags)
	}
	settings := map[string]string{}
	for name, attr := range f.Body.(*hclsyntax.Body).Attributes {
		if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String && v.IsKnown() && !v.IsNull() {
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
// dir that a merge writes (see writeMergedRoot), by path in dir with
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
	candidates := []string{ReadmeFileName, ".gitignore", varsDirName + "/" + commonVarsFileName}
	for _, env := range envs {
		for _, name := range []string{envVarsFileName, importVarsFileName, backendConfigFileName} {
			candidates = append(candidates, varsDirName+"/"+env+"/"+name)
		}
	}
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

// VerifyMerged runs the verification gate on the root environments share
// in dir as environment env plans it, with tf, a Terraform runner with
// env's credentials: with its var files, vars/common.tfvars,
// vars/<env>/terraform.tfvars and, until it is deleted after the first
// apply, vars/<env>/imports.tfvars, whose every import the plan must
// make. It initialises the root without its backend, as Verify does.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file.
func VerifyMerged(ctx context.Context, tf Terraform, dir, env string, opts Options) (Gate, error) {
	tf = withTimeout(tf, opts.CommandTimeout)
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	vars, _, err := mergedVarFiles(dir, env)
	if err != nil {
		return nil, err
	}
	expected := 0
	if _, err := os.Stat(filepath.Join(dir, ImportsFileName)); err == nil {
		ids, err := MergedImportIDs(dir, env)
		if err != nil {
			return nil, err
		}
		expected = len(ids)
	}
	return gateWith(ctx, tf, dir, opts, func() (Check, []string, error) {
		p, _, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return Check{}, nil, err
		}
		check := planCheck(p, diags, nil, opts.StateOnly, expected)
		check.Details = append([]string{"environment " + env}, check.Details...)
		return check, sensitiveValues(p), nil
	})
}

// mergedVarFiles returns the var files environment env plans the root
// environments share in dir with, as -var-file options and as absolute
// paths: those there are of vars/common.tfvars, vars/<env>/terraform.tfvars
// and, until it is deleted after the first apply,
// vars/<env>/imports.tfvars.
func mergedVarFiles(dir, env string) ([]tfexec.PlanOption, []string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	var vars []tfexec.PlanOption
	var paths []string
	for _, path := range []string{
		filepath.Join(abs, varsDirName, commonVarsFileName),
		filepath.Join(abs, varsDirName, env, envVarsFileName),
		filepath.Join(abs, varsDirName, env, importVarsFileName),
	} {
		if _, err := os.Stat(path); err == nil {
			vars = append(vars, tfexec.VarFile(path))
			paths = append(paths, path)
		}
	}
	return vars, paths, nil
}

// AddressType returns the resource type of an address, such as
// aws_sqs_queue for aws_sqs_queue.this["orders"] or
// module.logs.aws_s3_bucket.this[0].
func AddressType(address string) string {
	return addressType(address)
}

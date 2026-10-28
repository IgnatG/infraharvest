// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// mergedRootDir writes files, by path with forward slashes, into a new
// directory, and returns it.
func mergedRootDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The var files an environment plans with are those of VarFiles that are
// there, in their order: imports.tfvars counts until it is deleted.
func TestExistingVarFiles(t *testing.T) {
	t.Parallel()
	dir := mergedRootDir(t, map[string]string{
		"vars/common.tfvars":         "",
		"vars/dev/terraform.tfvars":  "env = \"dev\"\n",
		"vars/prod/terraform.tfvars": "env = \"prd\"\n",
		"vars/prod/imports.tfvars":   "import_ids = {}\n",
	})
	for env, want := range map[string][]string{
		"prod": {"vars/common.tfvars", "vars/prod/terraform.tfvars", "vars/prod/imports.tfvars"},
		"dev":  {"vars/common.tfvars", "vars/dev/terraform.tfvars"},
	} {
		if got := ExistingVarFiles(dir, env); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %v, want %v", env, got, want)
		}
	}
}

// An environment's backend settings are its backend.hcl, which must be
// there.
func TestBackendConfigFile(t *testing.T) {
	t.Parallel()
	dir := mergedRootDir(t, map[string]string{"vars/prod/backend.hcl": "bucket = \"acme-state\"\n"})
	path, err := BackendConfigFile(dir, "prod")
	if err != nil || !filepath.IsAbs(path) || filepath.ToSlash(path) != filepath.ToSlash(filepath.Join(dir, "vars", "prod", "backend.hcl")) {
		t.Errorf("prod: %q %v", path, err)
	}
	if _, err := BackendConfigFile(dir, "dev"); err == nil {
		t.Error("dev has no backend.hcl, but no error")
	}
}

// The environments are the directories under vars that hold a
// terraform.tfvars; their import IDs are those of their imports.tfvars.
func TestMergedImportIDs(t *testing.T) {
	t.Parallel()
	if envs, err := MergedEnvironments(t.TempDir()); err != nil || len(envs) != 0 {
		t.Errorf("a root of its own: %v %v", envs, err)
	}
	dir := mergedRootDir(t, map[string]string{
		"vars/dev/terraform.tfvars":  "env = \"dev\"\n",
		"vars/prod/terraform.tfvars": "env = \"prod\"\n",
		"vars/qa/backend.hcl":        "bucket = \"acme-state\"\n",
		"vars/prod/imports.tfvars": `import_ids = {
  "aws_sqs_queue.orders"            = "https://sqs.eu-west-2.amazonaws.com/2/shop-prd-orders"
  "aws_sqs_queue.this[\"refunds\"]" = "https://sqs.eu-west-2.amazonaws.com/2/shop-prd-refunds"
}
`,
	})
	envs, err := MergedEnvironments(dir)
	if err != nil || strings.Join(envs, ",") != "dev,prod" {
		t.Fatalf("environments: %v %v", envs, err)
	}
	ids, err := MergedImportIDs(dir, "prod")
	if err != nil || len(ids) != 2 || ids[`aws_sqs_queue.this["refunds"]`] != "https://sqs.eu-west-2.amazonaws.com/2/shop-prd-refunds" {
		t.Errorf("prod's import IDs: %v %v", ids, err)
	}
	if ids, err := MergedImportIDs(dir, "dev"); err != nil || ids != nil {
		t.Errorf("dev, without imports.tfvars: %v %v", ids, err)
	}
}

// A backend's settings are recognised by their path alone, so a handover
// keeps those of an environment whose values are gone.
func TestIsBackendConfig(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"vars/prod/backend.hcl":      true,
		"vars/backend.hcl":           false,
		"vars/prod/eu/backend.hcl":   false,
		"vars/prod/terraform.tfvars": false,
		"other/prod/backend.hcl":     false,
		"vars//backend.hcl":          false,
	} {
		if got := IsBackendConfig(name); got != want {
			t.Errorf("IsBackendConfig(%q) = %v, want %v", name, got, want)
		}
	}
}

// loadedVars writes files, a merged root's by path, and loads the var
// files of envs.
func loadedVars(t *testing.T, files map[string]string, envs ...string) MergedVars {
	t.Helper()
	vars, err := LoadMergedVars(mergedRootDir(t, files), envs)
	if err != nil {
		t.Fatal(err)
	}
	return vars
}

// setVar sets name to text for env, and returns env's var file as the
// edit leaves it.
func setVar(t *testing.T, vars MergedVars, env, name, text string) string {
	t.Helper()
	edit, ok := vars.Set(env, name, []byte(text))
	if !ok {
		t.Fatalf("%s: no var file of its own", env)
	}
	if want := EnvVarsPath(env); edit.File != want {
		t.Fatalf("%s: edits %s, want %s", env, edit.File, want)
	}
	src := vars.Own(env).Src
	return string(slices.Concat(src[:edit.Start], edit.Text, src[edit.End:]))
}

// An environment's own value goes over the one every environment shares,
// and a variable neither sets has none.
func TestMergedVarsPrecedence(t *testing.T) {
	t.Parallel()
	vars := loadedVars(t, map[string]string{
		"vars/common.tfvars":         "region = \"eu-west-2\"\ntimeout = 30\n",
		"vars/prod/terraform.tfvars": "timeout = 120\n",
		"vars/dev/terraform.tfvars":  "name = \"dev\"\n",
	}, "prod", "dev", "qa")
	for _, tc := range []struct{ env, name, file string }{
		{"prod", "timeout", "vars/prod/terraform.tfvars"},
		{"prod", "region", CommonVarsPath},
		{"dev", "timeout", CommonVarsPath},
		{"dev", "name", "vars/dev/terraform.tfvars"},
		{"qa", "region", CommonVarsPath},
	} {
		v, ok := vars.Value(tc.env, tc.name)
		if !ok || v.Path != tc.file || v.File != vars[tc.file] {
			t.Errorf("%s %s: %q %v, want %q", tc.env, tc.name, v.Path, ok, tc.file)
		}
	}
	if _, ok := vars.Value("prod", "name"); ok {
		t.Error("prod name: set, though only dev sets it")
	}
	if got := vars.environments(); !slices.Equal(got, []string{"dev", "prod"}) {
		t.Errorf("environments: %v, want those with a var file of their own", got)
	}
}

// Setting a variable for one environment replaces its own value, or adds
// one over the shared value, in its own file only; an environment without
// a file of its own can't be set.
func TestMergedVarsSet(t *testing.T) {
	t.Parallel()
	vars := loadedVars(t, map[string]string{
		"vars/common.tfvars":         "timeout = 30\n",
		"vars/prod/terraform.tfvars": "timeout = 120\nname = \"prod\"\n",
		"vars/dev/terraform.tfvars":  "name = \"dev\"\n",
	}, "prod", "dev", "qa")
	if got, want := setVar(t, vars, "prod", "timeout", "300"), "timeout = 300\nname = \"prod\"\n"; got != want {
		t.Errorf("prod, its own value replaced:\n%s\nwant:\n%s", got, want)
	}
	if got, want := setVar(t, vars, "dev", "timeout", "60"), "name = \"dev\"\ntimeout = 60\n"; got != want {
		t.Errorf("dev, over the shared value:\n%s\nwant:\n%s", got, want)
	}
	if _, ok := vars.Set("qa", "timeout", []byte("60")); ok {
		t.Error("qa: set, though it has no var file of its own")
	}
}

// A map one file sets replaces the other's whole, as Terraform reads var
// files, and setting it for an environment replaces its own whole literal.
func TestMergedVarsMaps(t *testing.T) {
	t.Parallel()
	vars := loadedVars(t, map[string]string{
		"vars/common.tfvars":         "tags = {\n  team  = \"core\"\n  owner = \"ops\"\n}\n",
		"vars/prod/terraform.tfvars": "tags = {\n  owner = \"sre\"\n}\n",
	}, "prod")
	want := cty.ObjectVal(map[string]cty.Value{"owner": cty.StringVal("sre")})
	if got := vars.Values("prod")["tags"]; !got.RawEquals(want) {
		t.Errorf("prod tags: %#v, want its own, with no shared key", got)
	}
	v, ok := vars.Value("prod", "tags")
	if _, isObject := v.Attr.Expr.(*hclsyntax.ObjectConsExpr); !ok || !isObject {
		t.Fatalf("prod tags: %v, want its own object literal", ok)
	}
	if got, want := setVar(t, vars, "prod", "tags", "{ owner = \"dba\" }"), "tags = { owner = \"dba\" }\n"; got != want {
		t.Errorf("prod tags set:\n%s\nwant:\n%s", got, want)
	}
}

// A line added to a var file starts on a line of its own, whether or not
// the file ends in a newline.
func TestMergedVarsNewlineGuard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, src, want string }{
		{"empty", "", "timeout = 60\n"},
		{"newline", "name = \"a\"\n", "name = \"a\"\ntimeout = 60\n"},
		{"no newline", "name = \"a\"", "name = \"a\"\ntimeout = 60\n"},
	} {
		vars := loadedVars(t, map[string]string{"vars/dev/terraform.tfvars": tc.src}, "dev")
		if got := setVar(t, vars, "dev", "timeout", "60"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A bool variable is an environment's own where its value, over the
// default, is true and every other environment's is false.
func TestMergedVarsOwnFlags(t *testing.T) {
	t.Parallel()
	vars := loadedVars(t, map[string]string{
		"vars/common.tfvars":         "deploy_shared = true\n",
		"vars/dev/terraform.tfvars":  "deploy_default = false\n",
		"vars/prod/terraform.tfvars": "deploy_alerts = true\ndeploy_unset = true\ndeploy_odd = \"yes\"\n",
		"vars/qa/terraform.tfvars":   "deploy_default = false\n",
	}, "dev", "prod", "qa")
	defaults := map[string]cty.Value{
		"deploy_alerts":  cty.False,
		"deploy_shared":  cty.False,
		"deploy_default": cty.True,
		"deploy_unset":   cty.NullVal(cty.Bool),
		"deploy_odd":     cty.False,
	}
	for env, want := range map[string]map[string]bool{
		"prod": {"deploy_alerts": true, "deploy_default": true},
		"dev":  {},
		"qa":   {},
	} {
		if got := vars.OwnFlags(env, defaults); !maps.Equal(got, want) {
			t.Errorf("%s: %v, want %v", env, got, want)
		}
	}
}

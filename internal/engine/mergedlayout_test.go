// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

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

// An environment plans with the shared values, then its own, then its
// import IDs; a root that imports nothing leaves those out.
func TestVarFileArgs(t *testing.T) {
	t.Parallel()
	if got, want := varFileArgs("prod", true), "-var-file=vars/common.tfvars -var-file=vars/prod/terraform.tfvars -var-file=vars/prod/imports.tfvars"; got != want {
		t.Errorf("with imports: %q, want %q", got, want)
	}
	if got, want := varFileArgs("prod", false), "-var-file=vars/common.tfvars -var-file=vars/prod/terraform.tfvars"; got != want {
		t.Errorf("without imports: %q, want %q", got, want)
	}
}

// The var files an environment plans with are those of varFiles that are
// there, absolute, in their order: imports.tfvars counts until it is
// deleted.
func TestMergedVarFiles(t *testing.T) {
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
		if got := existingVarFiles(dir, env); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: existing %v, want %v", env, got, want)
		}
		opts, paths, err := mergedVarFiles(dir, env)
		if err != nil {
			t.Fatal(err)
		}
		var rel []string
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				t.Errorf("%s: %s isn't absolute", env, path)
			}
			r, err := filepath.Rel(dir, path)
			if err != nil {
				t.Fatal(err)
			}
			rel = append(rel, filepath.ToSlash(r))
		}
		if strings.Join(rel, " ") != strings.Join(want, " ") || len(opts) != len(want) {
			t.Errorf("%s: %v, %d options, want %v", env, rel, len(opts), want)
		}
	}
}

// An environment's values are the shared ones, its own over them, and
// never its import IDs.
func TestMergedValues(t *testing.T) {
	t.Parallel()
	dir := mergedRootDir(t, map[string]string{
		"vars/common.tfvars":         "region = \"eu-west-2\"\ntimeout = 30\n",
		"vars/prod/terraform.tfvars": "timeout = 120\nname = \"orders-${var.env}\"\n",
		"vars/prod/imports.tfvars":   "import_ids = {\n  \"aws_sqs_queue.orders\" = \"q\"\n}\n",
	})
	values, err := mergedValues(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || !values["region"].RawEquals(cty.StringVal("eu-west-2")) || !values["timeout"].RawEquals(cty.NumberIntVal(120)) {
		t.Errorf("prod: %#v", values)
	}
	if values, err := mergedValues(dir, "dev"); err != nil || len(values) != 2 || !values["timeout"].RawEquals(cty.NumberIntVal(30)) {
		t.Errorf("dev, with only the shared values: %#v %v", values, err)
	}
}

// A feature's variable is deploy_<feature>, which tells it apart.
func TestFeatureFlagName(t *testing.T) {
	t.Parallel()
	if name := featureFlagName("alerts"); name != "deploy_alerts" || !isFeatureFlag(name) {
		t.Errorf("%q", name)
	}
	if isFeatureFlag("orders_timeout") {
		t.Error("orders_timeout is a feature's")
	}
}

// An environment's backend settings are its backend.hcl, which must be
// there.
func TestBackendConfigFile(t *testing.T) {
	t.Parallel()
	dir := mergedRootDir(t, map[string]string{"vars/prod/backend.hcl": "bucket = \"acme-state\"\n"})
	path, err := backendConfigFile(dir, "prod")
	if err != nil || !filepath.IsAbs(path) || filepath.ToSlash(path) != filepath.ToSlash(filepath.Join(dir, "vars", "prod", "backend.hcl")) {
		t.Errorf("prod: %q %v", path, err)
	}
	if _, err := backendConfigFile(dir, "dev"); err == nil {
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

// An environment's state is its workspace's, and the state its
// backend.hcl names, of any backend type a merged root writes.
func TestMergedStateLocations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "terraform.tfstate.d", "prod", "terraform.tfstate")
	for backend, want := range map[string]string{
		"": "",
		"bucket = \"acme-state\"\nkey = \"envs/prod/aws/2/eu-west-2/terraform.tfstate\"\nregion = \"eu-west-2\"\n":                                        "s3://acme-state/envs/prod/aws/2/eu-west-2/terraform.tfstate",
		"bucket = \"acme-state\"\nprefix = \"envs/prod/google/p/europe-west2\"\n":                                                                         "gs://acme-state/envs/prod/google/p/europe-west2/default.tfstate",
		"resource_group_name = \"state\"\nstorage_account_name = \"acme\"\ncontainer_name = \"tfstate\"\nkey = \"envs/prod/azure/s/terraform.tfstate\"\n": "https://acme.blob.core.windows.net/tfstate/envs/prod/azure/s/terraform.tfstate",
		"# The prod root had no s3 backend: set these.\n# bucket = \"\"\n":                                                                                "",
	} {
		path := filepath.Join(dir, filepath.FromSlash(backendConfigPath("prod")))
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if backend != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(backend), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		locations, err := MergedStateLocations(dir, "prod")
		wanted := []string{workspace}
		if want != "" {
			wanted = append(wanted, want)
		}
		if err != nil || strings.Join(locations, " ") != strings.Join(wanted, " ") {
			t.Errorf("%q: got %v, %v, want %v", backend, locations, err, wanted)
		}
	}
}

// MergedRootFiles returns the files a merge writes: the root's .tf files,
// README.md and its var files, not those of Terraform or of others.
func TestMergedRootFilesOnDisk(t *testing.T) {
	t.Parallel()
	files := map[string]string{}
	for _, name := range []string{"providers.tf", "sqs.tf", ReadmeFileName, ".gitignore", LockFileName, "vars/common.tfvars", "vars/dev/terraform.tfvars", "vars/dev/imports.tfvars", "vars/prod/terraform.tfvars", "vars/prod/backend.hcl", "vars/notes.md", "terraform.tfstate.d/dev/terraform.tfstate"} {
		files[name] = "# " + name + "\n"
	}
	got, err := MergedRootFiles(mergedRootDir(t, files))
	want := []string{".gitignore", ReadmeFileName, "providers.tf", "sqs.tf", "vars/common.tfvars", "vars/dev/imports.tfvars", "vars/dev/terraform.tfvars", "vars/prod/backend.hcl", "vars/prod/terraform.tfvars"}
	if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, %v, want %v", got, err, want)
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
		if got := isBackendConfig(name); got != want {
			t.Errorf("isBackendConfig(%q) = %v, want %v", name, got, want)
		}
	}
}

// A root without a backend keeps each environment's state in the
// workspace named after it, in Terraform's local backend's own layout.
func TestWorkspaceStateFile(t *testing.T) {
	t.Parallel()
	for env, want := range map[string]string{
		"prod": "root/terraform.tfstate.d/prod/terraform.tfstate",
		"dev":  "root/terraform.tfstate.d/dev/terraform.tfstate",
	} {
		if got := filepath.ToSlash(workspaceStateFile("root", env)); got != want {
			t.Errorf("%s: got %q, want %q", env, got, want)
		}
	}
}

// loadedVars writes files, a merged root's by path, and loads the var
// files of envs.
func loadedVars(t *testing.T, files map[string]string, envs ...string) mergedVars {
	t.Helper()
	vars, err := loadMergedVars(mergedRootDir(t, files), envs)
	if err != nil {
		t.Fatal(err)
	}
	return vars
}

// setVar sets name to text for env, and returns env's var file as the
// edit leaves it.
func setVar(t *testing.T, vars mergedVars, env, name, text string) string {
	t.Helper()
	edit, ok := vars.set(env, name, []byte(text))
	if !ok {
		t.Fatalf("%s: no var file of its own", env)
	}
	if want := envVarsPath(env); edit.File != want {
		t.Fatalf("%s: edits %s, want %s", env, edit.File, want)
	}
	src := vars.own(env).src
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
		{"prod", "region", commonVarsPath},
		{"dev", "timeout", commonVarsPath},
		{"dev", "name", "vars/dev/terraform.tfvars"},
		{"qa", "region", commonVarsPath},
	} {
		v, ok := vars.value(tc.env, tc.name)
		if !ok || v.file != tc.file || v.f != vars[tc.file] {
			t.Errorf("%s %s: %q %v, want %q", tc.env, tc.name, v.file, ok, tc.file)
		}
	}
	if _, ok := vars.value("prod", "name"); ok {
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
	if _, ok := vars.set("qa", "timeout", []byte("60")); ok {
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
	if got := vars.values("prod")["tags"]; !got.RawEquals(want) {
		t.Errorf("prod tags: %#v, want its own, with no shared key", got)
	}
	v, ok := vars.value("prod", "tags")
	if _, isObject := v.attr.Expr.(*hclsyntax.ObjectConsExpr); !ok || !isObject {
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
		if got := vars.ownFlags(env, defaults); !maps.Equal(got, want) {
			t.Errorf("%s: %v, want %v", env, got, want)
		}
	}
}

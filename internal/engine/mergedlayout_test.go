// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

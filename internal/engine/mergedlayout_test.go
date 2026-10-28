// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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
		path := filepath.Join(dir, filepath.FromSlash(hclfile.BackendConfigPath("prod")))
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
	for _, name := range []string{"providers.tf", "sqs.tf", rootfile.ReadmeFileName, ".gitignore", tfrun.LockFileName, "vars/common.tfvars", "vars/dev/terraform.tfvars", "vars/dev/imports.tfvars", "vars/prod/terraform.tfvars", "vars/prod/backend.hcl", "vars/notes.md", "terraform.tfstate.d/dev/terraform.tfstate"} {
		files[name] = "# " + name + "\n"
	}
	got, err := MergedRootFiles(mergedRootDir(t, files))
	want := []string{".gitignore", rootfile.ReadmeFileName, "providers.tf", "sqs.tf", "vars/common.tfvars", "vars/dev/imports.tfvars", "vars/dev/terraform.tfvars", "vars/prod/backend.hcl", "vars/prod/terraform.tfvars"}
	if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, %v, want %v", got, err, want)
	}
}

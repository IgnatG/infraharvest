// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
)

// generatedRoot is a root as an import generates it: the aws provider,
// declared and configured, a resource, a curated module, and a module the
// import generated, under the output directory.
var generatedRoot = map[string]string{
	"aws/1/eu-west-2/versions.tf": `terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}
`,
	"aws/1/eu-west-2/providers.tf": "provider \"aws\" {\n  region = \"eu-west-2\"\n}\n",
	"aws/1/eu-west-2/s3.tf":        "resource \"aws_s3_bucket\" \"logs\" {\n  bucket = \"logs\"\n}\n",
	"aws/1/eu-west-2/vpc.tf": `module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.0.0"
}

module "queues" {
  source = "../../../modules/queue_a1b2c3"
  providers = { aws = aws }
}
`,
	"modules/queue_a1b2c3/main.tf": "resource \"aws_sqs_queue\" \"this\" {\n  name = \"jobs\"\n}\n",
}

// writeTree writes files, by path relative to dir, under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The drift tools run Terraform only on roots whose providers are those
// infraharvest generates roots with, and whose modules are under the
// output directory or curated: anything else could run code of an
// agent's choosing with the server's credentials.
func TestCheckDriftRoots(t *testing.T) {
	const root = "aws/1/eu-west-2/"
	for name, tc := range map[string]struct {
		files map[string]string
		// want is in the error, "" for roots that pass.
		want string
	}{
		"a generated root": {nil, ""},
		"the public registry's host": {map[string]string{
			root + "versions.tf": "terraform {\n  required_providers {\n    aws = { source = \"registry.terraform.io/HashiCorp/aws\" }\n  }\n}\n",
			root + "vpc.tf":      "module \"vpc\" {\n  source = \"registry.terraform.io/terraform-aws-modules/vpc/aws\"\n}\n",
		}, ""},
		"a curated module's submodule": {map[string]string{root + "vpc.tf": "module \"role\" {\n  source = \"terraform-aws-modules/iam/aws//modules/iam-role\"\n}\n"}, ""},
		"the external data source":     {map[string]string{root + "x.tf": "data \"external\" \"x\" {\n  program = [\"sh\", \"-c\", \"id\"]\n}\n"}, "aws/1/eu-west-2/x.tf:1: uses the provider hashicorp/external"},
		"external declared": {map[string]string{
			root + "x.tf": "terraform {\n  required_providers {\n    external = { source = \"hashicorp/external\" }\n  }\n}\n",
		}, "declares the provider external as hashicorp/external"},
		"another provider as aws": {map[string]string{
			root + "versions.tf": "terraform {\n  required_providers {\n    aws = { source = \"attacker/aws\" }\n  }\n}\n",
		}, "declares the provider aws as attacker/aws"},
		"another registry": {map[string]string{
			root + "versions.tf": "terraform {\n  required_providers {\n    aws = { source = \"example.com/hashicorp/aws\" }\n  }\n}\n",
		}, "example.com/hashicorp/aws"},
		"a version alone":        {map[string]string{root + "x.tf": "terraform {\n  required_providers {\n    random = \"~> 3.0\"\n  }\n}\n"}, "declares the provider random as hashicorp/random"},
		"an implied provider":    {map[string]string{root + "x.tf": "resource \"null_resource\" \"x\" {}\n"}, "uses the provider hashicorp/null"},
		"a provider block":       {map[string]string{root + "x.tf": "provider \"local\" {}\n"}, "uses the provider hashicorp/local"},
		"an ephemeral resource":  {map[string]string{root + "x.tf": "ephemeral \"random_password\" \"x\" {}\n"}, "uses the provider hashicorp/random"},
		"a provider argument":    {map[string]string{root + "x.tf": "terraform {\n  required_providers {\n    evil = { source = \"attacker/evil\" }\n  }\n}\nresource \"aws_s3_bucket\" \"x\" {\n  provider = evil.west\n}\n"}, "attacker/evil"},
		"a provider of an alias": {map[string]string{root + "x.tf": "data \"aws_caller_identity\" \"x\" {\n  provider = tls.x\n}\n"}, "uses the provider hashicorp/tls"},
		"a check's data source":  {map[string]string{root + "x.tf": "check \"x\" {\n  data \"external\" \"x\" {\n    program = [\"id\"]\n  }\n  assert {\n    condition     = true\n    error_message = \"x\"\n  }\n}\n"}, "hashicorp/external"},
		"an import's provider":   {map[string]string{root + "x.tf": "import {\n  to       = aws_s3_bucket.logs\n  id       = \"logs\"\n  provider = http.x\n}\n"}, "uses the provider hashicorp/http"},
		"a module's providers":   {map[string]string{root + "x.tf": "module \"m\" {\n  source    = \"./m\"\n  providers = { aws = time.x }\n}\n"}, "uses the provider hashicorp/time"},
		"an override file": {map[string]string{
			root + "versions_override.tf": "terraform {\n  required_providers {\n    aws = { source = \"attacker/aws\" }\n  }\n}\n",
		}, "aws/1/eu-west-2/versions_override.tf:3: declares the provider aws as attacker/aws"},
		"a JSON file":      {map[string]string{root + "x.tf.json": `{"data": {"external": {"x": {"program": ["id"]}}}}`}, "aws/1/eu-west-2/x.tf.json:1: uses the provider hashicorp/external"},
		"an OpenTofu file": {map[string]string{root + "x.tofu": "data \"external\" \"x\" {}\n"}, "hashicorp/external"},
		"a file elsewhere": {map[string]string{"elsewhere/x.tf": "data \"external\" \"x\" {}\n"}, "elsewhere/x.tf:1"},
		"a module in git": {map[string]string{
			root + "x.tf": "module \"m\" {\n  source = \"git::https://example.com/m.git\"\n}\n",
		}, "the module m's source is git::https://example.com/m.git, which is neither under the output directory nor a curated module"},
		"a module over https": {map[string]string{root + "x.tf": "module \"m\" {\n  source = \"https://example.com/m.zip\"\n}\n"}, "https://example.com/m.zip"},
		"another registry module": {map[string]string{
			root + "x.tf": "module \"m\" {\n  source = \"attacker/thing/aws\"\n}\n",
		}, "attacker/thing/aws, which is neither"},
		"a module out of the output directory": {map[string]string{
			root + "x.tf": "module \"m\" {\n  source = \"../../../../elsewhere\"\n}\n",
		}, "the module m's source is ../../../../elsewhere, outside the output directory"},
		"a module source of a variable": {map[string]string{root + "x.tf": "module \"m\" {\n  source = var.m\n}\n"}, "Variables not allowed"},
		"an installed module": {map[string]string{
			root + ".terraform/modules/modules.json":   `{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"vpc","Source":"registry.terraform.io/terraform-aws-modules/vpc/aws","Version":"6.0.0","Dir":".terraform/modules/vpc"}]}`,
			root + ".terraform/modules/vpc/main.tf":    "resource \"aws_vpc\" \"this\" {}\n",
			root + ".terraform/modules/vpc/planted.tf": "data \"external\" \"x\" {}\n",
		}, "aws/1/eu-west-2/.terraform/modules/vpc/planted.tf:1: uses the provider hashicorp/external"},
		"an installed module out of the output directory": {map[string]string{
			root + ".terraform/modules/modules.json": `{"Modules":[{"Key":"vpc","Dir":"../../../../elsewhere"}]}`,
		}, "installs the module vpc in ../../../../elsewhere, outside the output directory"},
		"what Terraform downloaded beside the modules it uses": {map[string]string{
			root + ".terraform/modules/modules.json":            `{"Modules":[{"Key":"vpc","Dir":".terraform/modules/vpc"}]}`,
			root + ".terraform/modules/vpc/main.tf":             "resource \"aws_vpc\" \"this\" {}\n",
			root + ".terraform/modules/vpc/examples/x/main.tf":  "resource \"random_pet\" \"x\" {}\n",
			root + ".terraform/providers/registry/x/README.txt": "",
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			writeTree(t, out, generatedRoot)
			writeTree(t, out, tc.files)

			err := checkDriftRoots(out)

			switch {
			case tc.want == "" && err != nil:
				t.Errorf("got %v, want the roots to pass", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

// An output directory that isn't there has nothing to check: drift says
// it has no roots.
func TestCheckDriftRootsOfNoOutput(t *testing.T) {
	if err := checkDriftRoots(filepath.Join(t.TempDir(), "generated")); err != nil {
		t.Errorf("got %v, want nothing to check", err)
	}
}

// A symbolic link to a directory, which Terraform follows and the check
// doesn't, is refused.
func TestCheckDriftRootsRefusesLinkedDirectories(t *testing.T) {
	out, target := t.TempDir(), t.TempDir()
	writeTree(t, target, map[string]string{"x.tf": "data \"external\" \"x\" {}\n"})
	if err := os.Symlink(target, filepath.Join(out, "aws")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}

	if err := checkDriftRoots(out); err == nil || !strings.Contains(err.Error(), "aws is a symbolic link to a directory") {
		t.Errorf("got %v, want the link refused", err)
	}
}

// The providers the drift tools take are those of the providers compiled
// in and of their curated modules, never the external data source's.
func TestDriftAllowed(t *testing.T) {
	sources, modules := driftAllowed()
	for _, name := range providers.Names() {
		p := providers.New(name)
		source := p.Profile().Source
		if source == "" {
			source = "hashicorp/" + p.Name()
		}
		if !sources[providerAddress(source)] {
			t.Errorf("%s's provider %s isn't allowed: %v", name, source, sources)
		}
	}
	if sources[refusedProvider] {
		t.Errorf("%s is allowed", refusedProvider)
	}
	if !modules["terraform-aws-modules/vpc/aws"] || modules["terraform-aws-modules/iam/aws//modules/iam-role"] {
		t.Errorf("modules: %v, want each curated module by its address", modules)
	}
}

// Both drift tools refuse such roots before the user is asked, and tell
// them to run drift themselves if they trust the roots.
func TestDriftToolsRefuseOtherProviders(t *testing.T) {
	dir := t.TempDir()
	out := generatedOut(t, dir)
	writeTree(t, out, map[string]string{"aws/1/eu-west-2/x.tf": "data \"external\" \"x\" {\n  program = [\"id\"]\n}\n"})
	for tool, args := range map[string]map[string]any{
		"drift":        {"out": out},
		"drift_accept": {"out": out, "rules": []string{"accept all"}},
	} {
		t.Run(tool, func(t *testing.T) {
			runner := &fakeRunner{}
			asked := false
			session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = true
				return &mcp.ElicitResult{Action: "accept"}, nil
			})

			res, text := call(t, session, tool, args)

			if !res.IsError || !strings.Contains(text, "aws/1/eu-west-2/x.tf:1: uses the provider hashicorp/external") || !strings.Contains(text, "run infraharvest drift on them yourself from a terminal") {
				t.Errorf("got %s, want the roots refused", text)
			}
			if asked || len(runner.runs) != 0 {
				t.Errorf("asked %v, ran %v: want neither", asked, runner.runs)
			}
		})
	}
}

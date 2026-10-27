// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
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
	needProviders(t, "aws")
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
		// drift runs with --untrusted-roots, so Terraform ignores them.
		"what a .terraform directory has": {map[string]string{
			root + ".terraform/modules/modules.json":   `{"Modules":[{"Key":"vpc","Dir":"../../../../elsewhere"}]}`,
			root + ".terraform/modules/vpc/planted.tf": "data \"external\" \"x\" {}\n",
			root + ".terraform/providers/registry.terraform.io/hashicorp/aws/6.0.0/linux_amd64/terraform-provider-aws": "#!/bin/sh\n",
		}, ""},
		"a terraform.d directory": {map[string]string{
			root + "terraform.d/plugins/registry.terraform.io/hashicorp/aws/6.0.0/linux_amd64/terraform-provider-aws": "#!/bin/sh\n",
		}, "aws/1/eu-west-2/terraform.d is a terraform.d directory, which Terraform installs providers from"},
		"a module in a .terraform directory": {map[string]string{
			root + "x.tf":                         "module \"m\" {\n  source = \"./.terraform/modules/m\"\n}\n",
			root + ".terraform/modules/m/main.tf": "data \"external\" \"x\" {}\n",
		}, "the module m's source is ./.terraform/modules/m, in a .terraform directory"},
		"a module in a terraform.d directory": {map[string]string{
			root + "x.tf": "module \"m\" {\n  source = \"../../../terraform.d/m\"\n}\n",
		}, "the module m's source is ../../../terraform.d/m, in a terraform.d directory"},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			writeTree(t, out, generatedRoot)
			writeTree(t, out, tc.files)

			_, err := checkDriftRoots(out, newDriftAllowed(compiled))

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
	if _, err := checkDriftRoots(filepath.Join(t.TempDir(), "generated"), newDriftAllowed(compiled)); err != nil {
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

	if _, err := checkDriftRoots(out, newDriftAllowed(compiled)); err == nil || !strings.Contains(err.Error(), "aws is a symbolic link to a directory") {
		t.Errorf("got %v, want the link refused", err)
	}
}

// The providers the drift tools take are those of the providers compiled
// in and of their curated modules, never the external data source's.
func TestDriftAllowed(t *testing.T) {
	allowed := newDriftAllowed(compiled)
	sources, modules := allowed.sources, allowed.modules
	for _, name := range providers.Names() {
		p := providers.New(name)
		source := p.Profile().Source
		if source == "" {
			source = "hashicorp/" + p.Name()
		}
		want := []string{source}
		if m, ok := p.(harvest.ProviderWithModuleProviders); ok {
			for _, mp := range m.ModuleProviders() {
				want = append(want, mp.Source)
			}
		}
		for _, source := range want {
			if !sources[providerAddress(source)] {
				t.Errorf("%s's provider %s isn't allowed: %v", name, source, sources)
			}
		}
	}
	if sources[refusedProvider] {
		t.Errorf("%s is allowed", refusedProvider)
	}
	// aws's curated modules, where it is built in.
	if builtIn("aws") && (!modules["terraform-aws-modules/vpc/aws"] || modules["terraform-aws-modules/iam/aws//modules/iam-role"]) {
		t.Errorf("modules: %v, want each curated module by its address", modules)
	}
}

// The drift tools take the providers and curated modules of the
// providers the server is passed alone, those of the binary it runs.
func TestDriftAllowedOfTheProvidersPassed(t *testing.T) {
	needProviders(t, "aws")
	aws, err := compiled.profile("aws")
	if err != nil {
		t.Fatal(err)
	}
	allowed := newDriftAllowed(Providers{Profiles: []harvest.Profile{aws}, Sources: compiled.Sources, Check: compiled.Check})
	if want := map[string]bool{"hashicorp/aws": true}; !maps.Equal(allowed.sources, want) {
		t.Errorf("sources %v, want %v", allowed.sources, want)
	}
	for module := range allowed.modules {
		if !strings.HasPrefix(module, "terraform-aws-modules/") {
			t.Errorf("module %s, want only aws's curated modules", module)
		}
	}
}

// A confirmation covers the roots as they were checked: roots changed
// after the user was asked aren't run, nor checked again only, for both
// tools.
func TestDriftToolsRunTheConfirmedRoots(t *testing.T) {
	needProviders(t, "aws")
	for tool, args := range map[string]map[string]any{
		"drift":        {"out": "generated"},
		"drift_accept": {"out": "generated", "rules": []string{"accept all"}},
	} {
		for name, edit := range map[string]string{
			"an allowed change":        "resource \"aws_s3_bucket\" \"logs\" {\n  bucket = \"other\"\n}\n",
			"the external data source": "data \"external\" \"x\" {\n  program = [\"id\"]\n}\n",
		} {
			t.Run(tool+", "+name, func(t *testing.T) {
				dir := t.TempDir()
				writeTree(t, filepath.Join(dir, "generated"), generatedRoot)
				runner := &fakeRunner{}
				session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					writeTree(t, filepath.Join(dir, "generated"), map[string]string{"aws/1/eu-west-2/s3.tf": edit})
					return &mcp.ElicitResult{Action: "accept"}, nil
				})

				res, text := call(t, session, tool, args)

				if !res.IsError || !strings.Contains(text, "didn't confirm") && !strings.Contains(text, "hashicorp/external") {
					t.Errorf("got %s, want the run refused", text)
				}
				if len(runner.runs) != 0 {
					t.Errorf("ran %v, roots the user didn't confirm", runner.runs)
				}
			})
		}
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

// The check returns the digest of the files it read, the one drift
// checks the roots against, and refuses a file whose link leads out of
// the output directory, which the digest wouldn't cover as it is there.
func TestCheckDriftRootsDigest(t *testing.T) {
	needProviders(t, "aws")
	out := t.TempDir()
	writeTree(t, out, generatedRoot)

	digest, err := checkDriftRoots(out, newDriftAllowed(compiled))

	if want, werr := driftpolicy.RootsDigest(out); err != nil || werr != nil || digest != want {
		t.Errorf("got %q (%v), want drift's digest %q (%v)", digest, err, want, werr)
	}

	elsewhere := t.TempDir()
	writeTree(t, elsewhere, map[string]string{"x.tf": "data \"external\" \"x\" {}\n"})
	if err := os.Symlink(filepath.Join(elsewhere, "x.tf"), filepath.Join(out, "aws", "1", "eu-west-2", "x.tf")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if _, err := checkDriftRoots(out, newDriftAllowed(compiled)); err == nil || !strings.Contains(err.Error(), "aws/1/eu-west-2/x.tf is a symbolic link out of the output directory") {
		t.Errorf("got %v, want the link refused", err)
	}
}

// A module call that names a directory the walk read otherwise, such as
// through "..", or in another case where the file system ignores it, is
// that directory, which the walk checked; one the walk didn't read is
// refused, as its files would go unchecked and out of the digest.
func TestCheckDriftRootsModuleDirectories(t *testing.T) {
	needProviders(t, "aws")
	const root = "aws/1/eu-west-2/"
	for name, tc := range map[string]struct {
		source string
		// want is in the error, "" for roots that pass.
		want string
	}{
		"a directory read": {"../../../mods/x", ""},
		"through ..":       {"../../../mods/../mods/x", ""},
		"not there":        {"../../../mods/y", "the module directory mods/y can't be checked"},
		"in another case":  {"../../../MODS/X", ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			writeTree(t, out, generatedRoot)
			writeTree(t, out, map[string]string{
				"mods/x/main.tf": "resource \"aws_sqs_queue\" \"this\" {\n  name = \"jobs\"\n}\n",
				root + "x.tf":    "module \"m\" {\n  source = \"" + tc.source + "\"\n}\n",
			})
			want := tc.want
			if _, err := os.Stat(filepath.Join(out, "MODS", "X")); err != nil && name == "in another case" {
				// The file system tells case apart: there is no such
				// directory.
				want = "the module directory MODS/X can't be checked"
			}

			_, err := checkDriftRoots(out, newDriftAllowed(compiled))

			switch {
			case want == "" && err != nil:
				t.Errorf("got %v, want the roots to pass", err)
			case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
				t.Errorf("got %v, want an error with %q", err, want)
			}
		})
	}
}

// walkedDir finds the directory the walk read by the file it is, whatever
// the path names it, and refuses one with configuration files the walk
// didn't read.
func TestWalkedDir(t *testing.T) {
	out := t.TempDir()
	writeTree(t, out, map[string]string{"mods/x/main.tf": "# read\n", "mods/y/main.tf": "# not read\n", "mods/z/README.md": "# no configuration\n"})
	walked := filepath.Join(out, "mods", "x")
	files := map[string][]*hcl.File{walked: nil}

	if got, err := walkedDir(out, walked+string(filepath.Separator)+".", files); err != nil || got != walked {
		t.Errorf("got %q, %v, want %s", got, err, walked)
	}
	if _, err := walkedDir(out, filepath.Join(out, "mods", "y"), files); err == nil || !strings.Contains(err.Error(), "mods/y has configuration files the check didn't read") {
		t.Errorf("got %v, want the directory refused", err)
	}
	if got, err := walkedDir(out, filepath.Join(out, "mods", "z"), files); err != nil || got != filepath.Join(out, "mods", "z") {
		t.Errorf("got %q, %v, want a directory with nothing to check", got, err)
	}
}

// The roots' provider and backend blocks, and the backend settings drift
// initialises a root environments share with, may set only what
// infraharvest generates them with: anything else could have a provider
// or backend run a program or send its requests, with the credentials,
// elsewhere.
func TestCheckDriftRootsSettings(t *testing.T) {
	needProviders(t, "aws")
	const root = "aws/1/eu-west-2/"
	const google = "terraform {\n  required_providers {\n    google = { source = \"hashicorp/google\" }\n  }\n}\n"
	const azure = "terraform {\n  required_providers {\n    azurerm = { source = \"hashicorp/azurerm\" }\n    azapi = { source = \"Azure/azapi\" }\n  }\n}\n"
	for name, tc := range map[string]struct {
		files map[string]string
		// want is in the error, "" for roots that pass.
		want string
	}{
		"aws as generated": {map[string]string{root + "providers.tf": `provider "aws" {
  region              = "eu-west-2"
  allowed_account_ids = ["111111111111"]
  assume_role {
    role_arn = "arn:aws:iam::111111111111:role/read"
  }
  default_tags {
    tags = { Environment = "prod" }
  }
}

provider "aws" {
  alias  = "without_default_tags"
  region = "eu-west-2"
}
`}, ""},
		"google as generated": {map[string]string{root + "google.tf": google + "provider \"google\" {\n  project = \"p\"\n  region = \"europe-west2\"\n  add_terraform_attribution_label = false\n  default_labels = { env = \"prod\" }\n}\n"}, ""},
		"azure as generated":  {map[string]string{root + "azure.tf": azure + "provider \"azurerm\" {\n  features {}\n  subscription_id = \"s\"\n}\nprovider \"azapi\" {\n  subscription_id = \"s\"\n}\n"}, ""},
		"backends as generated": {map[string]string{
			root + "backend.tf":            "terraform {\n  backend \"s3\" {\n    bucket = \"b\"\n    key = \"k\"\n    region = \"eu-west-2\"\n    encrypt = true\n    kms_key_id = \"k\"\n    use_lockfile = true\n  }\n}\n",
			"gcp/backend.tf":               "terraform {\n  backend \"gcs\" {\n    bucket = \"b\"\n    prefix = \"p\"\n  }\n}\n",
			"azure/backend.tf":             "terraform {\n  backend \"azurerm\" {\n    resource_group_name = \"g\"\n    storage_account_name = \"a\"\n    container_name = \"c\"\n    key = \"k\"\n  }\n}\n",
			"merged/providers.tf":          "terraform {\n  backend \"s3\" {}\n}\n",
			"merged/vars/prod/backend.hcl": "bucket = \"b\"\nkey = \"k\"\nregion = \"eu-west-2\"\nencrypt = true\nuse_lockfile = true\n",
		}, ""},
		"aws shared config files":           {map[string]string{root + "providers.tf": "provider \"aws\" {\n  region = \"eu-west-2\"\n  shared_config_files = [\"./config\"]\n}\n"}, `aws/1/eu-west-2/providers.tf:3: the provider aws sets what infraharvest doesn't generate it with, which could have it run a program or send its requests elsewhere: An argument named "shared_config_files" is not expected here.`},
		"aws profile":                       {map[string]string{root + "providers.tf": "provider \"aws\" {\n  profile = \"planted\"\n}\n"}, `An argument named "profile"`},
		"aws endpoints":                     {map[string]string{root + "providers.tf": "provider \"aws\" {\n  endpoints {\n    sts = \"https://example.com\"\n  }\n}\n"}, `aws/1/eu-west-2/providers.tf:2: the provider aws sets what infraharvest doesn't generate it with, which could have it run a program or send its requests elsewhere: Blocks of type "endpoints" are not expected here.`},
		"aws custom_ca_bundle":              {map[string]string{root + "providers.tf": "provider \"aws\" {\n  custom_ca_bundle = \"ca.pem\"\n}\n"}, `"custom_ca_bundle"`},
		"aws sts_region":                    {map[string]string{root + "providers.tf": "provider \"aws\" {\n  sts_region = \"x\"\n}\n"}, `"sts_region"`},
		"aws assume_role's other arguments": {map[string]string{root + "providers.tf": "provider \"aws\" {\n  assume_role {\n    role_arn = \"r\"\n    source_identity = \"x\"\n  }\n}\n"}, `the provider aws's assume_role sets what infraharvest doesn't generate it with, which could have it run a program or send its requests elsewhere: An argument named "source_identity" is not expected here. It takes only role_arn`},
		"aws assume_role_with_web_identity": {map[string]string{root + "providers.tf": "provider \"aws\" {\n  assume_role_with_web_identity {\n    web_identity_token_file = \"t\"\n  }\n}\n"}, `"assume_role_with_web_identity"`},
		"a dynamic block":                   {map[string]string{root + "providers.tf": "provider \"aws\" {\n  dynamic \"endpoints\" {\n    for_each = [1]\n    content {}\n  }\n}\n"}, `"dynamic"`},
		"a JSON provider":                   {map[string]string{root + "x.tf.json": `{"provider": {"aws": {"endpoints": {"sts": "https://example.com"}}}}`}, `aws/1/eu-west-2/x.tf.json:1: the provider aws sets`},
		"an aliased local name":             {map[string]string{root + "x.tf": "terraform {\n  required_providers {\n    cloud = { source = \"hashicorp/aws\" }\n  }\n}\nprovider \"cloud\" {\n  profile = \"x\"\n}\n"}, `the provider cloud sets`},
		"azurerm client certificate":        {map[string]string{root + "azure.tf": azure + "provider \"azurerm\" {\n  features {}\n  client_certificate_path = \"c.pfx\"\n}\n"}, `"client_certificate_path"`},
		"azurerm metadata_host":             {map[string]string{root + "azure.tf": azure + "provider \"azurerm\" {\n  features {}\n  metadata_host = \"example.com\"\n}\n"}, `"metadata_host"`},
		"azurerm features":                  {map[string]string{root + "azure.tf": azure + "provider \"azurerm\" {\n  features {\n    key_vault {}\n  }\n}\n"}, `the provider azurerm's features sets`},
		"azapi endpoint":                    {map[string]string{root + "azure.tf": azure + "provider \"azapi\" {\n  environment = \"public\"\n}\n"}, `"environment"`},
		"google credentials":                {map[string]string{root + "google.tf": google + "provider \"google\" {\n  credentials = \"key.json\"\n}\n"}, `"credentials"`},
		"google custom endpoint":            {map[string]string{root + "google.tf": google + "provider \"google\" {\n  storage_custom_endpoint = \"https://example.com/\"\n}\n"}, `"storage_custom_endpoint"`},
		"an s3 backend's endpoints":         {map[string]string{root + "backend.tf": "terraform {\n  backend \"s3\" {\n    bucket = \"b\"\n    endpoints = { s3 = \"https://example.com\" }\n  }\n}\n"}, `aws/1/eu-west-2/backend.tf:4: the s3 backend sets what infraharvest doesn't generate it with`},
		"an azurerm backend's msi_endpoint": {map[string]string{"azure/backend.tf": "terraform {\n  backend \"azurerm\" {\n    msi_endpoint = \"https://example.com\"\n  }\n}\n"}, `"msi_endpoint"`},
		"a gcs backend's credentials":       {map[string]string{"gcp/backend.tf": "terraform {\n  backend \"gcs\" {\n    credentials = \"key.json\"\n  }\n}\n"}, `"credentials"`},
		"another backend":                   {map[string]string{root + "backend.tf": "terraform {\n  backend \"http\" {\n    address = \"https://example.com\"\n  }\n}\n"}, `aws/1/eu-west-2/backend.tf:2: the http backend, which infraharvest doesn't generate roots with (azurerm, gcs, s3)`},
		"a cloud block":                     {map[string]string{root + "backend.tf": "terraform {\n  cloud {\n    hostname = \"example.com\"\n  }\n}\n"}, `aws/1/eu-west-2/backend.tf:2: a cloud block`},
		"an environment's backend settings": {map[string]string{
			"merged/providers.tf":          "terraform {\n  backend \"s3\" {}\n}\n",
			"merged/vars/prod/backend.hcl": "bucket = \"b\"\nprofile = \"planted\"\n",
		}, `merged/vars/prod/backend.hcl:2: the s3 backend sets`},
	} {
		t.Run(name, func(t *testing.T) {
			// A root that declares another provider takes it built in.
			for _, content := range tc.files {
				if strings.HasPrefix(content, google) {
					needProviders(t, "google")
				}
				if strings.HasPrefix(content, azure) {
					needProviders(t, "azure")
				}
			}
			out := t.TempDir()
			writeTree(t, out, generatedRoot)
			writeTree(t, out, tc.files)

			_, err := checkDriftRoots(out, newDriftAllowed(compiled))

			switch {
			case tc.want == "" && err != nil:
				t.Errorf("got %v, want the roots to pass", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

// Every provider the drift tools take has the settings infraharvest
// configures it with, and the provider blocks and backends infraharvest
// writes pass the check.
func TestDriftSettingsOfWhatInfraharvestGenerates(t *testing.T) {
	sources := newDriftAllowed(compiled).sources
	for source := range sources {
		if providerSettings[source] == nil {
			t.Errorf("no settings for %s", source)
		}
	}
	var blocks []engine.Provider
	for _, name := range providers.Names() {
		p := providers.New(name)
		for local, config := range p.ProviderBlock()["provider"].(map[string]interface{}) {
			blocks = append(blocks, engine.Provider{Name: local, Config: config.(map[string]interface{})})
		}
		if m, ok := p.(harvest.ProviderWithModuleProviders); ok {
			for local, mp := range m.ModuleProviders() {
				blocks = append(blocks, engine.Provider{Name: local, Source: mp.Source, Config: mp.Config})
			}
		}
	}
	for _, p := range blocks {
		content, err := engine.ProvidersFile(p)
		if err != nil {
			t.Fatal(err)
		}
		source := p.Source
		if source == "" {
			source = "hashicorp/" + p.Name
		}
		checkGenerated(t, content, "provider", []string{"name"}, func(body hcl.Body) error {
			return checkSettings(body, providerSettings[providerAddress(source)], "the provider "+p.Name)
		})
	}
	for _, b := range []config.Backend{
		{S3: &config.S3{Bucket: "b", Region: "eu-west-2", KeyPrefix: "p", KMSKeyID: "k"}},
		{AzureRM: &config.AzureRM{ResourceGroupName: "g", StorageAccountName: "a", ContainerName: "c", KeyPrefix: "p"}},
		{GCS: &config.GCS{Bucket: "b", Prefix: "p"}},
	} {
		checkGenerated(t, b.File("aws/1/eu-west-2"), "terraform", nil, func(body hcl.Body) error {
			content, _, diags := body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "backend", LabelNames: []string{"type"}}}})
			if diags.HasErrors() || len(content.Blocks) != 1 {
				return fmt.Errorf("no backend: %v", diags)
			}
			return checkSettings(content.Blocks[0].Body, backendSettings[content.Blocks[0].Labels[0]], "the backend")
		})
	}
}

// checkGenerated checks the body of each block of type typ, with labels,
// in content with check.
func checkGenerated(t *testing.T, content []byte, typ string, labels []string, check func(hcl.Body) error) {
	t.Helper()
	f, diags := hclparse.NewParser().ParseHCL(content, "generated.tf")
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	body, _, diags := f.Body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: typ, LabelNames: labels}}})
	if diags.HasErrors() || len(body.Blocks) == 0 {
		t.Fatalf("%s: no %s block: %v", content, typ, diags)
	}
	for _, b := range body.Blocks {
		if err := check(b.Body); err != nil {
			t.Errorf("%s: %v", content, err)
		}
	}
}

// No expression in the roots may call a function that reads a local
// file: plan would show its content, such as credentials, to the agent
// as drift.
func TestCheckDriftRootsFileFunctions(t *testing.T) {
	needProviders(t, "aws")
	const root = "aws/1/eu-west-2/"
	for name, tc := range map[string]struct {
		files map[string]string
		// want is in the error, "" for roots that pass.
		want string
	}{
		"native syntax":                       {map[string]string{root + "x.tf": "resource \"aws_s3_bucket\" \"x\" {\n  bucket = file(\"~/.aws/credentials\")\n}\n"}, `aws/1/eu-west-2/x.tf:2: calls file, which reads a local file`},
		"in a template":                       {map[string]string{root + "x.tf": "resource \"aws_s3_bucket\" \"x\" {\n  bucket = \"logs-${filebase64(\"/etc/passwd\")}\"\n}\n"}, `x.tf:2: calls filebase64`},
		"in default_tags":                     {map[string]string{root + "providers.tf": "provider \"aws\" {\n  region = \"eu-west-2\"\n  default_tags {\n    tags = {\n      Owner = trimspace(templatefile(\"t.tpl\", {}))\n    }\n  }\n}\n"}, `providers.tf:5: calls templatefile`},
		"in a locals block":                   {map[string]string{root + "locals.tf": "locals {\n  keys = [for f in fileset(path.module, \"*\") : filesha256(f)]\n}\n"}, `locals.tf:2: calls fileset`},
		"namespaced":                          {map[string]string{root + "x.tf": "locals {\n  key = core::file(\"~/.ssh/id_rsa\")\n}\n"}, `x.tf:2: calls core::file`},
		"templatestring":                      {map[string]string{root + "x.tf": "locals {\n  t = templatestring(\"$${file(\\\"x\\\")}\", {})\n}\n"}, `calls templatestring`},
		"JSON":                                {map[string]string{root + "x.tf.json": "{\n  \"resource\": {\"aws_s3_bucket\": {\"x\": {\n    \"bucket\": \"${file(\\\"~/.aws/credentials\\\")}\"\n  }}}\n}\n"}, `aws/1/eu-west-2/x.tf.json:3: calls file`},
		"JSON in default_tags":                {map[string]string{root + "x.tf.json": "{\"provider\": {\"aws\": {\"default_tags\": {\"tags\": {\"Owner\": \"${core::filesha512(\\\"k\\\")}\"}}}}}"}, `x.tf.json:1: calls core::filesha512`},
		"JSON escaped":                        {map[string]string{root + "x.tf.json": `{"locals": {"k": "${file(\"k\")}"}}`}, `x.tf.json:1: calls file`},
		"a JSON key":                          {map[string]string{root + "x.tf.json": `{"locals": {"k": {"${fileexists(\"k\")}": 1}}}`}, `calls fileexists`},
		"a JSON string that isn't a template": {map[string]string{root + "x.tf.json": `{"locals": {"k": "${"}}`}, `x.tf.json:1: a string that isn't a template`},
		"a JSON literal":                      {map[string]string{root + "x.tf.json": `{"locals": {"k": "file(\"k\") and $${file(\"k\")}"}}`}, ""},
		"other functions":                     {map[string]string{root + "x.tf": "locals {\n  k = upper(format(\"%s\", provider::aws::arn_parse(\"x\").region))\n}\n"}, ""},
		"a var file, which can't":             {map[string]string{root + "terraform.tfvars": "k = file(\"k\")\n"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			writeTree(t, out, generatedRoot)
			writeTree(t, out, tc.files)

			_, err := checkDriftRoots(out, newDriftAllowed(compiled))

			switch {
			case tc.want == "" && err != nil:
				t.Errorf("got %v, want the roots to pass", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

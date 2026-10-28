// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// vacatedFixture writes a merged root of dev and prod into dir, with a
// backend, and returns the files its merge wrote.
func vacatedFixture(t *testing.T, dir string) []string {
	t.Helper()
	files := map[string]string{
		rootfile.ProvidersFileName:   "terraform {\n  required_version = \">= 1.5, < 2.0\"\n\n  backend \"s3\" {}\n}\n\nprovider \"aws\" {\n  region = var.aws_region\n\n  default_tags {\n    tags = {\n      Environment = var.env\n    }\n  }\n}\n",
		hclfile.VariablesFileName:    "variable \"env\" {\n  description = \"The environment.\"\n  type        = string\n\n  validation {\n    condition     = contains([\"dev\", \"prd\"], var.env)\n    error_message = \"env must be one of dev, prd.\"\n  }\n}\n\nvariable \"aws_region\" {\n  description = \"The region.\"\n  type        = string\n}\n\nvariable \"orders_timeout\" {\n  description = \"The orders queue's visibility timeout.\"\n  type        = number\n}\n\nvariable \"import_ids\" {\n  description = \"The import IDs.\"\n  type        = map(string)\n  default     = {}\n}\n",
		rootfile.LocalsFileName:      "locals {\n  naming_prefix = \"shop-${var.env}\"\n}\n",
		"sqs.tf":                     "resource \"aws_sqs_queue\" \"orders\" {\n  name                       = \"${local.naming_prefix}-orders\"\n  visibility_timeout_seconds = var.orders_timeout\n}\n",
		rootfile.ImportsFileName:     "import {\n  to = aws_sqs_queue.orders\n  id = var.import_ids[\"aws_sqs_queue.orders\"]\n}\n",
		"vars/common.tfvars":         "# Values every environment shares.\n",
		"vars/dev/terraform.tfvars":  "# The dev environment's values.\nenv            = \"dev\"\naws_region     = \"us-east-1\"\norders_timeout = 90\n",
		"vars/dev/imports.tfvars":    "import_ids = {\n  \"aws_sqs_queue.orders\" = \"https://sqs/shop-dev-orders\"\n}\n",
		"vars/dev/backend.hcl":       "key = \"envs/dev/aws/us-east-1/terraform.tfstate\"\n",
		"vars/prod/terraform.tfvars": "# The prod environment's values.\nenv            = \"prd\"\naws_region     = \"us-east-1\"\norders_timeout = 30\n",
		"vars/prod/imports.tfvars":   "import_ids = {\n  \"aws_sqs_queue.orders\" = \"https://sqs/shop-prd-orders\"\n}\n",
		"vars/prod/backend.hcl":      "key = \"envs/prod/aws/us-east-1/terraform.tfstate\"\n",
		rootfile.ReadmeFileName:      "# aws/us-east-1\n",
		".gitignore":                 "!vars/**/*.tfvars\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// A merged root all of whose resources go to other roots keeps only what
// each environment plans it with to forget them: providers.tf, its
// required_version raised to what removed blocks need, variables.tf and
// the var files with only what providers.tf reads, each environment's
// backend settings, and moves.tf with the removed blocks. Its code,
// imports.tf and the environments' import IDs go, and its README says to
// apply it as each environment before the roots they go to.
func TestVacateMerged(t *testing.T) {
	dir := t.TempDir()
	files := vacatedFixture(t, dir)
	removed := []rootfile.Removal{{From: "aws_sqs_queue.orders", Resources: []rootfile.Departure{
		{Type: "aws_sqs_queue", ID: "https://sqs/shop-dev-orders", Address: "aws_sqs_queue.orders", Root: "envs/dev/aws/sqs/us-east-1"},
		{Type: "aws_sqs_queue", ID: "https://sqs/shop-prd-orders", Address: "aws_sqs_queue.orders", Root: "envs/prod/aws/sqs/us-east-1"},
	}}}
	left, check, err := VacateMerged(dir, files, removed)
	if err != nil || !check.Passed {
		t.Fatalf("VacateMerged: %+v, %v", check, err)
	}
	want := []string{".gitignore", rootfile.ReadmeFileName, rootfile.MovesFileName, rootfile.ProvidersFileName, hclfile.VariablesFileName, "vars/common.tfvars", "vars/dev/backend.hcl", "vars/dev/terraform.tfvars", "vars/prod/backend.hcl", "vars/prod/terraform.tfvars"}
	if !slices.Equal(left, want) {
		t.Errorf("left %v, want %v", left, want)
	}
	for _, name := range []string{"sqs.tf", rootfile.ImportsFileName, rootfile.LocalsFileName, "vars/dev/imports.tfvars", "vars/prod/imports.tfvars"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
	variables := readFile(t, dir, hclfile.VariablesFileName)
	if !strings.Contains(variables, `variable "env"`) || !strings.Contains(variables, `variable "aws_region"`) || strings.Contains(variables, "orders_timeout") || strings.Contains(variables, "import_ids") {
		t.Errorf("variables.tf:\n%s", variables)
	}
	tfvars := readFile(t, dir, "vars/dev/terraform.tfvars")
	if !strings.Contains(tfvars, `"dev"`) || !strings.Contains(tfvars, "aws_region") || strings.Contains(tfvars, "orders_timeout") {
		t.Errorf("vars/dev/terraform.tfvars:\n%s", tfvars)
	}
	if providers := readFile(t, dir, rootfile.ProvidersFileName); !strings.Contains(providers, `required_version = ">= 1.10, < 2.0"`) {
		t.Errorf("providers.tf doesn't require what removed blocks need:\n%s", providers)
	}
	if gone, err := rootfile.ReadRemoved(dir); err != nil || len(gone) != 1 || gone[0].From != "aws_sqs_queue.orders" {
		t.Errorf("removed blocks: %+v, %v", gone, err)
	}
	if moves := readFile(t, dir, rootfile.MovesFileName); !strings.Contains(moves, "destroy = false") {
		t.Errorf("moves.tf:\n%s", moves)
	}
	readme := readFile(t, dir, rootfile.ReadmeFileName)
	for _, want := range []string{"# Handed over resources", "dev, prod", "terraform init -reconfigure -backend-config=vars/dev/backend.hcl", "-var-file=vars/common.tfvars -var-file=vars/dev/terraform.tfvars", "| `aws_sqs_queue.orders` | `envs/prod/aws/sqs/us-east-1` |", "## Checks"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md has no %q:\n%s", want, readme)
		}
	}
	if strings.Contains(readme, "imports.tfvars") {
		t.Errorf("README.md still names the import IDs:\n%s", readme)
	}
}

// A merged root whose moves.tf still moves resources isn't vacated: its
// moved blocks may not have been applied in every environment yet.
func TestVacateMergedStillMoving(t *testing.T) {
	dir := t.TempDir()
	files := vacatedFixture(t, dir)
	if err := os.WriteFile(filepath.Join(dir, rootfile.MovesFileName), []byte("moved {\n  from = aws_sqs_queue.old\n  to   = aws_sqs_queue.orders\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := VacateMerged(dir, append(files, rootfile.MovesFileName), []rootfile.Removal{{From: "aws_sqs_queue.orders"}}); err == nil || !strings.Contains(err.Error(), "still moves") {
		t.Errorf("VacateMerged: %v, want it refused", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sqs.tf")); err != nil {
		t.Errorf("sqs.tf: %v", err)
	}
}

// What an environment plans the root with is kept while it is listed:
// the backend settings of an environment whose values are gone, and the
// shared values, cut down to what providers.tf reads, with no environment
// left, whose README then names none.
func TestVacateMergedGoneEnvironments(t *testing.T) {
	removed := []rootfile.Removal{{From: "aws_sqs_queue.orders"}}
	for name, tc := range map[string]struct {
		gone   []string
		want   []string
		readme string
	}{
		"backend of an environment without values": {
			gone:   []string{"vars/prod/terraform.tfvars"},
			want:   []string{".gitignore", rootfile.ReadmeFileName, rootfile.MovesFileName, rootfile.ProvidersFileName, hclfile.VariablesFileName, "vars/common.tfvars", "vars/dev/backend.hcl", "vars/dev/terraform.tfvars", "vars/prod/backend.hcl"},
			readme: "for dev.",
		},
		"shared values without environments": {
			gone:   []string{"vars/dev/terraform.tfvars", "vars/prod/terraform.tfvars"},
			want:   []string{".gitignore", rootfile.ReadmeFileName, rootfile.MovesFileName, rootfile.ProvidersFileName, hclfile.VariablesFileName, "vars/common.tfvars", "vars/dev/backend.hcl", "vars/prod/backend.hcl"},
			readme: "terraform init -reconfigure -backend-config=vars/<env>/backend.hcl",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files := vacatedFixture(t, dir)
			if err := os.WriteFile(filepath.Join(dir, "vars", "common.tfvars"), []byte("aws_region     = \"us-east-1\"\norders_timeout = 60\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, gone := range tc.gone {
				if err := os.Remove(filepath.Join(dir, filepath.FromSlash(gone))); err != nil {
					t.Fatal(err)
				}
			}
			left, check, err := VacateMerged(dir, files, removed)
			if err != nil || !check.Passed {
				t.Fatalf("VacateMerged: %+v, %v", check, err)
			}
			if !slices.Equal(left, tc.want) {
				t.Errorf("left %v, want %v", left, tc.want)
			}
			if common := readFile(t, dir, "vars/common.tfvars"); !strings.Contains(common, "aws_region") || strings.Contains(common, "orders_timeout") {
				t.Errorf("vars/common.tfvars:\n%s", common)
			}
			if readme := readFile(t, dir, rootfile.ReadmeFileName); !strings.Contains(readme, tc.readme) {
				t.Errorf("README.md has no %q:\n%s", tc.readme, readme)
			}
		})
	}
}

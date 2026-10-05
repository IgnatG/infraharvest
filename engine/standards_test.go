// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedVersions = `terraform {
  required_version = ">= 1.16, < 2.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.14"
    }
  }
}
`

// standardRoot writes a root that follows the output standard, with a
// local module, and returns the root and the module.
func standardRoot(t *testing.T) (string, string) {
	t.Helper()
	out := t.TempDir()
	root := filepath.Join(out, "aws", "123", "eu-west-2")
	module := filepath.Join(out, "modules", "s3_bucket_0123abcd")
	for _, dir := range []string{root, module} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, root, VersionsFileName, pinnedVersions)
	writeConfig(t, root, GeneratedFileName, `resource "aws_s3_bucket" "logs" {
  bucket = "logs-${var.suffix}"
}

module "state" {
  source = "../../../modules/s3_bucket_0123abcd"
  bucket = "state"
}

module "artifacts" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "5.16.1"
}
`)
	writeConfig(t, root, VariablesFileName, `variable "suffix" {
  description = "A secret."
  type        = string
  sensitive   = true
}
`)
	writeConfig(t, module, "main.tf", "resource \"aws_s3_bucket\" \"this\" {\n  bucket = var.bucket\n}\n")
	writeConfig(t, module, "variables.tf", "variable \"bucket\" {\n  description = \"The bucket.\"\n  type        = string\n}\n")
	writeConfig(t, module, "outputs.tf", "output \"id\" {\n  description = \"The id.\"\n  value       = aws_s3_bucket.this.id\n}\n")
	writeConfig(t, module, VersionsFileName, "terraform {\n  required_version = \">= 1.16\"\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \">= 6.14\"\n    }\n  }\n}\n")
	writeConfig(t, module, ReadmeFileName, "# s3_bucket_0123abcd\n")
	return root, module
}

var omitACL = map[string][]string{"aws_s3_bucket": {"acl"}}

func TestCheckStandardsPasses(t *testing.T) {
	root, _ := standardRoot(t)

	check, err := checkStandards(root, omitACL)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed {
		t.Errorf("want a pass, got %v", check.Details)
	}
}

func TestCheckStandardsFindings(t *testing.T) {
	for rule, breakIt := range map[string]func(t *testing.T, root, module string){
		"unpinned-required-version": func(t *testing.T, root, _ string) {
			writeConfig(t, root, VersionsFileName, strings.Replace(pinnedVersions, ">= 1.16, < 2.0", ">= 1.16", 1))
		},
		"unpinned-provider-version": func(t *testing.T, root, _ string) {
			writeConfig(t, root, VersionsFileName, strings.Replace(pinnedVersions, "~> 6.14", ">= 6.14", 1))
		},
		"missing-required-version": func(t *testing.T, _, module string) {
			writeConfig(t, module, VersionsFileName, "terraform {\n}\n")
		},
		"untyped-variable": func(t *testing.T, _, module string) {
			writeConfig(t, module, "variables.tf", "variable \"bucket\" {\n  description = \"The bucket.\"\n}\n")
		},
		"undocumented-output": func(t *testing.T, _, module string) {
			writeConfig(t, module, "outputs.tf", "output \"id\" {\n  value = aws_s3_bucket.this.id\n}\n")
		},
		"invalid-block": func(t *testing.T, _, module string) {
			// A hand-edited root with a label-less block must fail, not
			// panic.
			writeConfig(t, module, "outputs.tf", "output {\n  value = aws_s3_bucket.this.id\n}\n")
		},
		"secret-persisted-to-state": func(t *testing.T, root, _ string) {
			writeConfig(t, root, VariablesFileName, "variable \"suffix\" {\n  description = \"A secret.\"\n  type        = string\n  sensitive   = true\n  default     = \"x\"\n}\n")
		},
		"legacy-interpolation": func(t *testing.T, root, _ string) {
			writeConfig(t, root, "locals.tf", "locals {\n  name = \"${var.suffix}\"\n}\n")
		},
		"unpinned-module-version": func(t *testing.T, root, _ string) {
			content := strings.Replace(readFile(t, root, GeneratedFileName), `version = "5.16.1"`, `version = "~> 5.16"`, 1)
			writeConfig(t, root, GeneratedFileName, content)
		},
		"deprecated-argument": func(t *testing.T, root, _ string) {
			content := strings.Replace(readFile(t, root, GeneratedFileName), `bucket = "logs-${var.suffix}"`, "bucket = \"logs-${var.suffix}\"\n  acl    = \"private\"", 1)
			writeConfig(t, root, GeneratedFileName, content)
		},
		"committed-state-file": func(t *testing.T, root, _ string) {
			if err := os.WriteFile(filepath.Join(root, "terraform.tfstate"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"cross-dialect-syntax": func(t *testing.T, root, _ string) {
			if err := os.WriteFile(filepath.Join(root, "main.tofu"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"module-structure": func(t *testing.T, _, module string) {
			if err := os.Remove(filepath.Join(module, ReadmeFileName)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(rule, func(t *testing.T) {
			root, module := standardRoot(t)
			breakIt(t, root, module)

			check, err := checkStandards(root, omitACL)
			if err != nil {
				t.Fatal(err)
			}
			if check.Passed || len(check.Details) != 1 || !strings.HasPrefix(check.Details[0], rule+": ") {
				t.Errorf("want one %s finding, got %v", rule, check.Details)
			}
		})
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// required_version is what the configuration needs: 1.5 for its import
// blocks, a later release for a later feature, or what a module it calls
// requires, not the version of the Terraform that generated it.
func TestRequireMinimumVersion(t *testing.T) {
	t.Parallel()
	const versions = "terraform {\n  required_version = \">= 1.16, < 2.0\"\n}\n"
	for name, tt := range map[string]struct {
		files   map[string]string
		modules map[string]string // the required_version of each installed module
		want    string
	}{
		"import blocks": {
			files: map[string]string{ImportsFileName: "import {\n  to = aws_sqs_queue.a\n  id = \"a\"\n}\n"},
			want:  `">= 1.5, < 2.0"`,
		},
		"removed blocks": {
			files: map[string]string{"main.tf": "removed {\n  from = aws_sqs_queue.a\n}\n"},
			want:  `">= 1.7, < 2.0"`,
		},
		// Terraform takes them from 1.7, OpenTofu from 1.10.
		"removed blocks with destroy = false": {
			files: map[string]string{MovesFileName: "removed {\n  from = aws_sqs_queue.a\n\n  lifecycle {\n    destroy = false\n  }\n}\n"},
			want:  `">= 1.10, < 2.0"`,
		},
		// Terraform takes them from 1.1, OpenTofu from 1.6.
		"moved blocks": {
			files: map[string]string{MovesFileName: "moved {\n  from = aws_sqs_queue.a\n  to   = aws_sqs_queue.b\n}\n"},
			want:  `">= 1.5, < 2.0"`,
		},
		"import blocks by identity": {
			files: map[string]string{ImportsFileName: "import {\n  to = aws_cloudwatch_log_group.a\n  identity = {\n    name = \"a\"\n  }\n}\n"},
			want:  `">= 1.12, < 2.0"`,
		},
		"write-only arguments": {
			files: map[string]string{"ssm.tf": "resource \"aws_ssm_parameter\" \"a\" {\n  name     = \"a\"\n  value_wo = var.a\n}\n"},
			want:  `">= 1.11, < 2.0"`,
		},
		"write-only arguments left unset": {
			files: map[string]string{"ssm.tf": "resource \"aws_ssm_parameter\" \"a\" {\n  value_wo = null\n}\n"},
			want:  `">= 1.5, < 2.0"`,
		},
		"modules": {
			files:   map[string]string{"main.tf": "module \"vpc\" {\n  source = \"terraform-aws-modules/vpc/aws\"\n}\n"},
			modules: map[string]string{"vpc": ">= 1.5.7", "vpc.sub": ">= 1.3"},
			want:    `">= 1.5.7, < 2.0"`,
		},
		// Terraform still lists the module of a call the plan rejected.
		"modules no longer called": {
			files:   map[string]string{"main.tf": "module \"bucket\" {\n  source = \"terraform-aws-modules/s3-bucket/aws\"\n}\n"},
			modules: map[string]string{"bucket": ">= 1.3", "vpc": ">= 1.5.7"},
			want:    `">= 1.5, < 2.0"`,
		},
	} {
		dir := t.TempDir()
		writeConfig(t, dir, VersionsFileName, versions)
		for file, content := range tt.files {
			writeConfig(t, dir, file, content)
		}
		if len(tt.modules) > 0 {
			var entries []string
			for key, required := range tt.modules {
				moduleDir := filepath.Join(dir, ".terraform", "modules", key)
				if err := os.MkdirAll(moduleDir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeConfig(t, moduleDir, VersionsFileName, "terraform {\n  required_version = \""+required+"\"\n}\n")
				entries = append(entries, `{"Key":"`+key+`","Source":"x","Dir":".terraform/modules/`+key+`"}`)
			}
			manifest := `{"Modules":[{"Key":"","Source":"","Dir":"."},` + strings.Join(entries, ",") + `]}`
			if err := os.WriteFile(filepath.Join(dir, ".terraform", "modules", "modules.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		if err := requireMinimumVersion(dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := readFile(t, dir, VersionsFileName); !strings.Contains(got, "required_version = "+tt.want) {
			t.Errorf("%s: got\n%s\nwant required_version = %s", name, got, tt.want)
		}
	}
}

func TestRequireMinimumVersionWithoutVersionsFile(t *testing.T) {
	t.Parallel()
	if err := requireMinimumVersion(t.TempDir()); err != nil {
		t.Error(err)
	}
}

// A root whose init kept its working files elsewhere, its TF_DATA_DIR,
// as drift's of roots that may come from someone else does, has its
// modules read from there: a downloaded module by the absolute path
// Terraform lists it at, a local one relative to the root. The root's own
// .terraform isn't read.
func TestInstalledModulesIn(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vpc := filepath.Join(data, "modules", "vpc")
	write(filepath.Join(data, "modules", "modules.json"), fmt.Sprintf(`{"Modules":[{"Key":"","Dir":"."},{"Key":"vpc","Dir":%q},{"Key":"queue","Dir":"../../modules/queue"}]}`, filepath.ToSlash(vpc)))
	write(filepath.Join(dir, ".terraform", "modules", "modules.json"), `{"Modules":[{"Key":"planted","Dir":".terraform/modules/planted"}]}`)

	got, err := installedModulesIn(dir, data)

	want := []installedModule{{key: "vpc", dir: vpc}, {key: "queue", dir: filepath.Join(dir, "..", "..", "modules", "queue")}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %v, want %v", got, err, want)
	}
	if got, err := installedModulesIn(dir, ""); err != nil || len(got) != 1 || got[0].key != "planted" {
		t.Errorf("without a data directory: got %v, %v, want the root's .terraform", got, err)
	}
}

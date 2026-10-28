// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// Resources go into their imports' files, module calls into their first
// resource's; generated.tf goes once it is empty.
func TestSplitFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_iam_role" "app" {
  name = "app"
}

resource "aws_subnet" "a" {
  vpc_id = module.main.vpc_id
}

module "main" {
  source = "terraform-aws-modules/vpc/aws"
}
`)
	imports := []rootfile.Import{
		{Type: "aws_iam_role", Name: "app", File: "iam.tf"},
		{Type: "aws_subnet", Name: "a", File: "network.tf"},
		{Type: "aws_vpc", Name: "main", File: "network.tf"},
	}
	calls := []ModuleCall{{Name: "main", Resources: []string{"aws_vpc.main"}}}

	files, err := splitFiles(dir, imports, calls)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"iam.tf", "network.tf"}; !reflect.DeepEqual(files, want) {
		t.Errorf("files: %v, want %v", files, want)
	}
	if got := readFile(t, dir, "iam.tf"); !strings.HasPrefix(got, `resource "aws_iam_role" "app"`) {
		t.Errorf("iam.tf:\n%s", got)
	}
	network := readFile(t, dir, "network.tf")
	if !strings.Contains(network, `resource "aws_subnet" "a"`) || !strings.Contains(network, `module "main"`) {
		t.Errorf("network.tf:\n%s", network)
	}
	if _, err := os.Stat(filepath.Join(dir, rootfile.GeneratedFileName)); !os.IsNotExist(err) {
		t.Errorf("generated.tf is still there: %v", err)
	}
}

// Without files to go to, and for reserved names, everything stays.
func TestSplitFilesKeepsWhatNamesNoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, "resource \"aws_iam_role\" \"app\" {\n}\n\nresource \"aws_sqs_queue\" \"jobs\" {\n}\n")
	files, err := splitFiles(dir, []rootfile.Import{{Type: "aws_iam_role", Name: "app", File: "iam.tf"}, {Type: "aws_sqs_queue", Name: "jobs", File: rootfile.ImportsFileName}}, nil)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); !strings.Contains(got, `"aws_sqs_queue" "jobs"`) || strings.Contains(got, "aws_iam_role") {
		t.Errorf("generated.tf:\n%s", got)
	}
}

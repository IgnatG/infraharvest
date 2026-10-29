// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
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

// scaleGenerated writes a generated.tf of n instances and n/10 subnets,
// in reverse order, as Terraform's graph walk might, and returns their
// imports, the instances' in compute.tf and the subnets' in network.tf.
func scaleGenerated(b *testing.B, dir string, n int) []rootfile.Import {
	b.Helper()
	var src strings.Builder
	var imports []rootfile.Import
	for i := n - 1; i >= 0; i-- {
		fmt.Fprintf(&src, "# __generated__ by Terraform\nresource \"aws_instance\" \"i%d\" {\n  ami           = \"ami-1\"\n  instance_type = \"t3.micro\"\n}\n\n", i)
		imports = append(imports, rootfile.Import{Type: "aws_instance", Name: fmt.Sprintf("i%d", i), File: "compute.tf"})
		if i%10 == 0 {
			fmt.Fprintf(&src, "resource \"aws_subnet\" \"s%d\" {\n  cidr_block = \"10.0.0.0/24\"\n}\n\n", i)
			imports = append(imports, rootfile.Import{Type: "aws_subnet", Name: fmt.Sprintf("s%d", i), File: "network.tf"})
		}
	}
	if err := os.WriteFile(filepath.Join(dir, rootfile.GeneratedFileName), []byte(src.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	return imports
}

// BenchmarkSortAndSplit sorts the blocks of a large generated.tf and
// splits them into files, as every generation of a large root does.
func BenchmarkSortAndSplit(b *testing.B) {
	for _, n := range []int{10000, 20000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				dir := b.TempDir()
				imports := scaleGenerated(b, dir, n)
				b.StartTimer()
				ws := rootWorkspace(b, dir)
				if _, err := annotate(ws, []annotation{{name: "resources sorted", edit: hclfile.SortResources}}); err != nil {
					b.Fatal(err)
				}
				flushed(b, ws)
				if _, err := splitFiles(dir, imports, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

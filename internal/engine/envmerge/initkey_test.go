// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// The versions file the schemas are read with needs of init what a merged
// root of the same provider's resources needs, and a module call, another
// provider or another version needs init again.
func TestInitKey(t *testing.T) {
	t.Parallel()
	aws := rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"}
	versions := map[string][]byte{rootfile.VersionsFileName: rootfile.VersionsFile(">= 1.5.0", aws)}
	base, ok := initKey(versions)
	if !ok {
		t.Fatal("no key for the versions file")
	}
	merged := map[string][]byte{
		rootfile.ProvidersFileName:  []byte("terraform {\n  required_version = \">= 1.7, < 2.0\"\n\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \"~> 6.14\"\n    }\n  }\n}\n\nprovider \"aws\" {\n  region = var.aws_region\n}\n"),
		"sqs.tf":                    []byte("resource \"aws_sqs_queue\" \"orders\" {\n  name = \"orders\"\n}\n\ndata \"aws_vpc\" \"main\" {\n  provider = aws.other\n}\n"),
		"vars/dev/terraform.tfvars": []byte("x = 1\n"),
	}
	if got, ok := initKey(merged); !ok || got != base {
		t.Errorf("merged root: %q, %v; want %q", got, ok, base)
	}
	for name, files := range map[string]map[string][]byte{
		"a module call":    {"main.tf": []byte("module \"q\" {\n  source = \"./modules/q\"\n}\n")},
		"another provider": {"main.tf": []byte("resource \"random_id\" \"x\" {\n  byte_length = 4\n}\n")},
		"another version":  {rootfile.VersionsFileName: rootfile.VersionsFile(">= 1.5.0", rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.20"})},
	} {
		all := maps.Clone(versions)
		maps.Copy(all, files)
		if got, ok := initKey(all); ok && got == base {
			t.Errorf("%s: same key %q", name, got)
		}
	}
}

// initCounter counts the inits of a Terraform runner.
type initCounter struct {
	tfrun.Terraform
	inits *int
}

func (c *initCounter) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	*c.inits++
	return c.Terraform.Init(ctx, opts...)
}

// A merge inits once when the merged root needs nothing init installs
// that the schemas' init didn't, over as many rounds as it takes, and
// counts each environment's plans under the merge step.
func TestMergeRootsInitsOnceAndCountsPlans(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		changed map[string]map[string]int
		rounds  int
	}{
		"one round": {rounds: 1},
		// prod's first plan of the shared queue would update it.
		"two rounds": {changed: map[string]map[string]int{"prod": {"aws_sqs_queue.orders": 1}}, rounds: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
			var overrides []string
			roots := mergeRoots(t, dir, tt.changed, &overrides)
			inits := 0
			for i := range roots {
				next := roots[i].Terraform
				roots[i].Terraform = func(d string) (tfrun.Terraform, error) {
					tf, err := next(d)
					return &initCounter{Terraform: tf, inits: &inits}, err
				}
			}
			result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"], Gate: testGate})
			if err != nil {
				t.Fatal(err)
			}
			if result.Reason != "" {
				t.Fatalf("not merged: %s", result.Reason)
			}
			if inits != 1 {
				t.Errorf("%d inits, want 1", inits)
			}
			want := []tfrun.StepPlans{{Step: StepMerge, Plans: tt.rounds, Shows: tt.rounds}}
			if len(result.Plans) != 2 {
				t.Fatalf("plans: %+v", result.Plans)
			}
			for e, plans := range result.Plans {
				if !slices.Equal(plans, want) {
					t.Errorf("environment %d: %+v, want %+v", e, plans, want)
				}
			}
		})
	}
}

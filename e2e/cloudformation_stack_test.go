// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// TestAWSSelectionCloudFormation imports the CloudFormation stack of
// testdata/aws with --cfn-stack, without --services or a selection file:
// it must list only the services of the stack's types, import the stack's
// bucket, which the default selection leaves out as the stack's, and
// nothing else, leaving out the rest as outside the scope; and, as the
// bucket has no DeletionPolicy, report the stack as not safe to delete,
// with a copy of its template that retains the bucket.
func TestAWSSelectionCloudFormation(t *testing.T) {
	e := seedAWS(t)
	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "aws",
		"--engine=" + e.engineName,
		"--terraform-path=" + e.execPath,
		"--regions=us-east-1",
		"--cfn-stack=infraharvest-e2e-app",
		"--out=" + out,
		"--config=" + stateBackendConfig(t),
		"--allow-partial",
	})
	if err := root.ExecuteContext(e.ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import: %v", err)
	}
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	var imported []string
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			imported = append(imported, report.ResourceType(res.Address)+" "+res.ID)
		}
	}
	if !slices.Contains(imported, "aws_s3_bucket infraharvest-e2e-cfn-assets") {
		t.Errorf("the stack's bucket wasn't imported: %v", imported)
	}
	for _, res := range imported {
		if !strings.HasPrefix(res, "aws_s3_bucket") {
			t.Errorf("imported %s, which isn't the stack's", res)
		}
	}
	for _, x := range r.Excluded {
		if x.ID != "infraharvest-e2e-cfn-assets" && !strings.HasPrefix(x.Reason, report.OutsideScopePrefix) {
			t.Errorf("%s %s excluded as %q, not as outside the scope", x.Type, x.ID, x.Reason)
		}
	}
	if len(r.CloudFormationStacks) != 1 {
		t.Fatalf("stacks %+v", r.CloudFormationStacks)
	}
	s := r.CloudFormationStacks[0]
	if s.Name != "infraharvest-e2e-app" || len(s.Resources) != 1 || s.Resources[0].Outcome != report.StackImported {
		t.Errorf("stack %+v", s)
	}
	if s.SafeToDelete || len(s.Resources[0].Retain) == 0 {
		t.Errorf("a stack whose bucket isn't retained is safe to delete: %+v", s)
	}
	content, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(s.RetainTemplate)))
	if err != nil || !strings.Contains(string(content), `"DeletionPolicy": "Retain"`) {
		t.Errorf("template with Retain %q: %q, %v", s.RetainTemplate, content, err)
	}
}

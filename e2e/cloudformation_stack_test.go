// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
	"github.com/IgnatG/infraharvest/internal/selection"
	"github.com/IgnatG/infraharvest/internal/stackintent"
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
			imported = append(imported, engine.AddressType(res.Address)+" "+res.ID)
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
	if s.Name != "infraharvest-e2e-app" || len(s.Resources) != 1 || s.Resources[0].Outcome != cfn.StackImported {
		t.Errorf("stack %+v", s)
	}
	if s.SafeToDelete || len(s.Resources[0].Retain) == 0 {
		t.Errorf("a stack whose bucket isn't retained is safe to delete: %+v", s)
	}
	content, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(s.RetainTemplate)))
	if err != nil || !strings.Contains(string(content), `"DeletionPolicy": "Retain"`) {
		t.Errorf("template with Retain %q: %q, %v", s.RetainTemplate, content, err)
	}

	// The template's parameter and output are in the bucket's root, and its
	// gate, which checked them, passed.
	var bucketRoot string
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			if res.ID == "infraharvest-e2e-cfn-assets" {
				bucketRoot = d.Path
				for _, c := range d.Checks {
					if !c.Passed {
						t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
					}
				}
			}
		}
	}
	intent, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(bucketRoot), stackintent.FileName("infraharvest-e2e-app")))
	if err != nil {
		t.Fatalf("the stack's template in %s: %v", bucketRoot, err)
	}
	for _, want := range []string{`variable "bucket_name"`, `default     = "infraharvest-e2e-cfn-assets"`, `output "assets_arn"`, `.arn`} {
		if !strings.Contains(string(intent), want) {
			t.Errorf("%s lacks %s:\n%s", stackintent.FileName("infraharvest-e2e-app"), want, intent)
		}
	}
	uses := false
	tfFiles, _ := filepath.Glob(filepath.Join(out, filepath.FromSlash(bucketRoot), "*.tf"))
	for _, f := range tfFiles {
		content, _ := os.ReadFile(f)
		uses = uses || regexp.MustCompile(`\bbucket\s+= var\.bucket_name\b`).Match(content)
	}
	if !uses {
		t.Errorf("no argument of %s refers to var.bucket_name", bucketRoot)
	}
}

// TestAWSSelectionCloudFormationMCP drives the MCP server's discover and
// import tools with cfn_stacks, as an agent does, on the CloudFormation
// stack of testdata/aws, without services: discover lists the services
// of the stack's types into a selection file, the stack's bucket
// included, though the default selection leaves out what a stack
// manages. import asks the user, naming the stack and saying it lists the
// services of its resources, and imports the bucket and its
// configuration alone, reporting the stack as not safe to delete, as
// TestAWSSelectionCloudFormation does from the command line.
func TestAWSSelectionCloudFormationMCP(t *testing.T) {
	e := seedAWS(t)
	root := t.TempDir()
	var asked []string
	session := mcpSession(e.ctx, t, root, e.execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	scope := map[string]any{"provider": "aws", "regions": []string{"us-east-1"}, "cfn_stacks": []string{"infraharvest-e2e-app"}, "selection": "selection.yaml"}

	var summary mcpserver.SelectionSummary
	callTool(e.ctx, t, session, "discover", scope, &summary)
	f, err := selection.Load(filepath.Join(root, "selection.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if bucket := f.Resources[selectionEntry(t, f, "aws_s3_bucket", "infraharvest-e2e-cfn-assets")]; !bucket.Include {
		t.Errorf("discover: the stack's bucket isn't included: %+v", bucket)
	}
	for _, r := range f.Resources {
		if r.Include && r.ID != "infraharvest-e2e-cfn-assets" {
			t.Errorf("discover: %s %s, which isn't the stack's, is included", r.Type, r.ID)
		}
	}
	if len(asked) != 0 {
		t.Errorf("discover asked the user: %q", asked)
	}

	var imported mcpserver.ImportResult
	importArgs := maps.Clone(scope)
	importArgs["out"], importArgs["engine"] = "generated", e.engineName
	callTool(e.ctx, t, session, "import", importArgs, &imported)
	if len(asked) != 1 || !strings.Contains(asked[0], "Import the 1 resources") ||
		!strings.Contains(asked[0], "the aws account in us-east-1, only the resources of CloudFormation stacks infraharvest-e2e-app (the services of the stacks' resources)") {
		t.Errorf("import asked the user %q, want one confirmation of importing the stack's bucket", asked)
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported == 0 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import: %+v", imported)
	}
	r, err := report.Read(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	// The bucket and its configuration, as from the command line.
	var resources []string
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			resources = append(resources, engine.AddressType(res.Address)+" "+res.ID)
		}
	}
	if !slices.Contains(resources, "aws_s3_bucket infraharvest-e2e-cfn-assets") {
		t.Errorf("import: the stack's bucket wasn't imported: %v", resources)
	}
	for _, res := range resources {
		if !strings.HasPrefix(res, "aws_s3_bucket") {
			t.Errorf("import: imported %s, which isn't the stack's", res)
		}
	}
	if len(r.CloudFormationStacks) != 1 {
		t.Fatalf("stacks %+v", r.CloudFormationStacks)
	}
	s := r.CloudFormationStacks[0]
	if s.Name != "infraharvest-e2e-app" || len(s.Resources) != 1 || s.Resources[0].Outcome != cfn.StackImported || s.SafeToDelete {
		t.Errorf("stack %+v, want its bucket imported, and it not safe to delete", s)
	}
}

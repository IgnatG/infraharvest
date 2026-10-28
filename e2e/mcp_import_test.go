// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// mcpImportAccount is the account TestAWSMCPImport creates
// testdata/aws-drift-queues in, which no other test uses.
const mcpImportAccount = "767676767676"

// mcpImportProfile is the profile TestAWSMCPImport lists
// mcpImportAccount as.
const mcpImportProfile = "infraharvest-e2e-mcp-import"

// TestAWSMCPImport drives the MCP server's import tool, as an agent does,
// into an output directory again: with incremental, and with regenerate,
// and with a configuration file. testdata/aws-drift-queues has two queues
// and the bucket the roots keep their state in. discover lists the queues,
// and the agent excludes the jobs queue. An import whose configuration
// file sets terraform-path, which names a binary to run, is refused before
// the user is asked, and writes nothing. One whose configuration file
// gives the roots an S3 backend in the bucket asks the user, saying so,
// and imports the orders queue into a root with that backend. With the
// jobs queue included again, import with incremental asks to add what the
// roots don't have yet, and adds the jobs queue alone, reporting the
// orders queue as already in the root. With regenerate, it asks to
// generate the roots again, and generates both queues, with nothing to
// move.
func TestAWSMCPImport(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixture := seedApplied(ctx, t, "aws-drift-queues", execPath, plugins, tfexec.Var("account="+mcpImportAccount))
	bucket := stateValue(t, fixture.resources(ctx, t), "aws_s3_bucket", "state", "bucket")
	useAWSAccount(t, mcpImportAccount, mcpImportProfile)

	// The tools' files stay under root.
	root := t.TempDir()
	var asked []string
	session := mcpSession(ctx, t, root, execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	scope := func(more map[string]any) map[string]any {
		args := map[string]any{"provider": "aws", "services": []string{"sqs"}, "regions": []string{"us-east-1"}, "profile": mcpImportProfile}
		maps.Copy(args, more)
		return args
	}
	const reads = "the aws account with profile " + mcpImportProfile + " in us-east-1 (sqs)"

	var summary mcpserver.SelectionSummary
	callTool(ctx, t, session, "discover", scope(map[string]any{"selection": "selection.yaml"}), &summary)
	selectionFile := filepath.Join(root, "selection.yaml")
	if summary.Included != 2 || summary.Excluded != 0 {
		t.Errorf("discover: %+v, want the two queues included", summary)
	}
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	jobs := selectionEntry(t, f, "aws_sqs_queue", "-jobs")
	orders := selectionEntry(t, f, "aws_sqs_queue", "-orders")
	f.Resources[jobs].Include = false
	if err := f.Save(selectionFile); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "generated")
	importArgs := func(more map[string]any) map[string]any {
		args := scope(map[string]any{"selection": "selection.yaml", "out": "generated", "engine": engineName})
		maps.Copy(args, more)
		return args
	}

	// A configuration file that names a binary to run is refused before
	// the user is asked, and nothing runs.
	writeRootFile(t, root, "terraform-path.yaml", "version: 1\nsettings:\n  terraform-path: /bin/sh\n")
	if text := callToolError(ctx, t, session, "import", importArgs(map[string]any{"config": "terraform-path.yaml"})); !strings.Contains(text, "terraform-path isn't a setting the import tool takes") {
		t.Errorf("import with terraform-path in the configuration file: %s", text)
	}
	if len(asked) != 0 {
		t.Errorf("import with terraform-path in the configuration file asked the user: %q", asked)
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("import with terraform-path in the configuration file wrote %s: %v", out, err)
	}

	// A configuration file with an S3 backend asks the user, saying so,
	// and the roots keep their state in the bucket.
	backend := "version: 1\nbackend:\n  s3:\n    bucket: " + bucket + "\n    region: us-east-1\n    key_prefix: e2e\n"
	writeRootFile(t, root, "backend.yaml", backend)
	withBackend := func(more map[string]any) map[string]any {
		args := importArgs(map[string]any{"config": "backend.yaml"})
		maps.Copy(args, more)
		return args
	}
	var imported mcpserver.ImportResult
	callTool(ctx, t, session, "import", withBackend(nil), &imported)
	if len(asked) != 1 || !strings.Contains(asked[0], "Import the 1 resources") || !strings.Contains(asked[0], reads) ||
		!strings.Contains(asked[0], `The configuration file "`+filepath.Join(root, "backend.yaml")+`" applies, setting backend`) || !strings.Contains(asked[0], `"`+bucket+`"`) {
		t.Errorf("import asked the user %q, want one confirmation of importing the one queue of %s, with the backend in %s", asked, reads, bucket)
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported != 1 || imported.Totals.Excluded != 1 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import: %+v", imported)
	}
	checkCoverage(t, "the import with a backend", coverageOf(t, out), nil)
	dirs := generatedDirs(t, out)
	if len(dirs) != 1 {
		t.Fatalf("the import generated the roots %v, want one for the account's us-east-1", dirs)
	}
	for _, dir := range dirs {
		if content := readFile(t, filepath.Join(dir, importer.BackendFileName)); !strings.Contains(content, bucket) {
			t.Errorf("%s: the backend isn't the bucket %s:\n%s", dir, bucket, content)
		}
	}
	importBlocks := func() string {
		var b strings.Builder
		for _, dir := range generatedDirs(t, out) {
			b.WriteString(readFile(t, filepath.Join(dir, rootfile.ImportsFileName)))
		}
		return b.String()
	}
	if blocks := importBlocks(); !strings.Contains(blocks, f.Resources[orders].ID) || strings.Contains(blocks, f.Resources[jobs].ID) {
		t.Errorf("the import's import blocks import other than the orders queue alone:\n%s", blocks)
	}

	// With the jobs queue included again, incremental adds it alone.
	f.Resources[jobs].Include = true
	if err := f.Save(selectionFile); err != nil {
		t.Fatal(err)
	}
	before := outputFiles(t, out)
	imported = mcpserver.ImportResult{}
	callTool(ctx, t, session, "import", withBackend(map[string]any{"incremental": true}), &imported)
	if len(asked) != 2 || !strings.Contains(asked[1], "Add the resources that") || !strings.Contains(asked[1], "don't have yet to those roots") || !strings.Contains(asked[1], reads) {
		t.Errorf("import with incremental asked the user %q, want a confirmation of adding what the roots don't have", asked[1:])
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported != 1 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import with incremental: %+v", imported)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import with incremental", coverage, nil)
	if !excludedIn(coverage, "aws_sqs_queue", f.Resources[orders].ID) {
		t.Errorf("import with incremental: the orders queue isn't reported as already in the root: %+v", coverage.Excluded)
	}
	checkAddedTo(t, out, dirs[0], "aws_sqs_queue", "infraharvest-e2e-drift-"+mcpImportAccount+"-jobs", before, outputFiles(t, out))
	if blocks := importBlocks(); !strings.Contains(blocks, f.Resources[jobs].ID) {
		t.Errorf("import with incremental didn't add the jobs queue:\n%s", blocks)
	}

	// With regenerate, it generates the roots again, with both queues
	// where they were.
	imported = mcpserver.ImportResult{}
	callTool(ctx, t, session, "import", withBackend(map[string]any{"regenerate": true}), &imported)
	if len(asked) != 3 || !strings.Contains(asked[2], `Generate the roots in "`+out+`" again, from scratch, from the 2 resources`) || !strings.Contains(asked[2], reads) {
		t.Errorf("import with regenerate asked the user %q, want a confirmation of generating the roots again", asked[2:])
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported != 2 || len(imported.Moved) != 0 || len(imported.Departed) != 0 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import with regenerate: %+v", imported)
	}
	checkCoverage(t, "the import with regenerate", coverageOf(t, out), nil)
	if blocks := importBlocks(); !strings.Contains(blocks, f.Resources[orders].ID) || !strings.Contains(blocks, f.Resources[jobs].ID) {
		t.Errorf("import with regenerate doesn't import both queues:\n%s", blocks)
	}
	if got := generatedDirs(t, out); len(got) != len(dirs) {
		t.Errorf("import with regenerate: roots %v, want %v", got, dirs)
	}
}

// writeRootFile writes content to name under root, the directory the MCP
// server's files stay under.
func writeRootFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

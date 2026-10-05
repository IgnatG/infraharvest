// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
)

// fakeRunner records the arguments of each run, and writes a selection
// file for discover and prints a report for import. With discoverExit set,
// discover fails with that code and discoverStderr instead.
type fakeRunner struct {
	runs           [][]string
	discoverExit   int
	discoverStderr string
}

func (f *fakeRunner) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	f.runs = append(f.runs, args)
	switch args[0] {
	case "discover":
		if f.discoverExit != 0 {
			return nil, []byte(f.discoverStderr), f.discoverExit, nil
		}
		path := strings.TrimPrefix(args[len(args)-1], "--selection=")
		return nil, nil, 0, writeSelection(path)
	case "import":
		r := report.Report{Coverage: report.Coverage{Totals: report.CoverageTotal{Discovered: 3, Imported: 2, Excluded: 1}}}
		out, err := json.Marshal(r)
		return out, []byte("imported 2 of 3 resources\n"), 0, err
	}
	return nil, nil, 2, nil
}

func writeSelection(path string) error {
	f := &selection.File{Version: selection.Version, Resources: []selection.Resource{
		{Type: "aws_subnet", ID: "subnet-1", Include: true},
		{Type: "aws_vpc", ID: "vpc-1", Include: true},
		{Type: "aws_vpc", ID: "vpc-default", Reason: "the default VPC"},
	}}
	return f.Save(path)
}

// connect returns a client session with the server, whose files stay under
// root; elicit answers the server's confirmation requests, or nil for a
// client that can't.
func connect(t *testing.T, runner *fakeRunner, root string, elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := New("test", root, runner.run).Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, &mcp.ClientOptions{ElicitationHandler: elicit})
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text = append(text, tc.Text)
		}
	}
	return res, strings.Join(text, "\n")
}

func TestDiscover(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)
	path := filepath.Join(dir, "selection.yaml")

	res, text := call(t, session, "discover", map[string]any{"provider": "aws", "resources": []string{"vpc", "subnet"}, "regions": []string{"eu-west-2"}, "selection": path})

	if res.IsError || !strings.Contains(text, "lists 3 resources: 2 included, 1 excluded") {
		t.Errorf("result: %s", text)
	}
	want := []string{"discover", "aws", "--resources=vpc,subnet", "--regions=eu-west-2", "--selection=" + path}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
	summary, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(summary), "aws_vpc vpc-default: the default VPC") {
		t.Errorf("summary misses the exclusion: %s", summary)
	}
}

// When discover couldn't run, nothing was written: a selection file from
// an earlier run must not pass for its result.
func TestDiscoverCouldNotRun(t *testing.T) {
	runner := &fakeRunner{discoverExit: report.ExitCouldNotRun, discoverStderr: "aws: no credentials found\n"}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}

	res, text := call(t, session, "discover", map[string]any{"provider": "aws", "resources": []string{"vpc"}, "selection": path})

	if !res.IsError || !strings.Contains(text, "no credentials found") || strings.Contains(text, "lists 3 resources") {
		t.Errorf("want an error result with the command's output, got %s", text)
	}
}

func importArgs(selectionPath, output string) map[string]any {
	return map[string]any{"provider": "aws", "resources": []string{"vpc"}, "regions": []string{"eu-west-2"}, "profile": "prod", "selection": selectionPath, "output": output}
}

func TestImportAfterTheUserConfirms(t *testing.T) {
	runner := &fakeRunner{}
	var asked string
	dir := t.TempDir()
	session := connect(t, runner, dir, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = req.Params.Message
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "generated")

	res, text := call(t, session, "import", importArgs(path, out))

	if res.IsError || !strings.Contains(text, "Imported 2 of 3 resources") {
		t.Errorf("result: %s", text)
	}
	// The user sees what the import reads before confirming.
	for _, want := range []string{"Import the 2 resources", "never applies", "profile prod", "eu-west-2", "vpc"} {
		if !strings.Contains(asked, want) {
			t.Errorf("confirmation %q misses %q", asked, want)
		}
	}
	want := []string{"import", "aws", "--resources=vpc", "--regions=eu-west-2", "--profile=prod", "--engine=terraform", "--selection=" + path, "--path-output=" + out, "--output=json"}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
}

func TestImportNeedsTheUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	decline := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	for name, tc := range map[string]struct {
		elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)
		want   string
	}{
		"declined":                {decline, "didn't confirm"},
		"a client that can't ask": {nil, "infraharvest import aws --resources=vpc"},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{}
			session := connect(t, runner, dir, tc.elicit)

			res, text := call(t, session, "import", importArgs(path, filepath.Join(dir, "generated")))

			if !res.IsError || !strings.Contains(text, tc.want) {
				t.Errorf("want an error result with %q, got %s", tc.want, text)
			}
			if len(runner.runs) != 0 {
				t.Errorf("imported without confirmation: %v", runner.runs)
			}
		})
	}
}

func TestScopeArgsKeepValuesOutOfFlags(t *testing.T) {
	for _, provider := range []string{"--help", "aws --all", "", "AWS"} {
		if _, err := scopeArgs("import", Scope{Provider: provider, Resources: []string{"vpc"}}); err == nil {
			t.Errorf("provider %q accepted", provider)
		}
	}
	args, err := scopeArgs("import", Scope{Provider: "aws", Resources: []string{"--all"}, Profile: "x --y"})
	if err != nil || !slices.Equal(args, []string{"import", "aws", "--resources=--all", "--profile=x --y"}) {
		t.Errorf("args: %v, %v", args, err)
	}
}

// A confirmation only counts for the import the user was asked about.
func TestImportChecksWhatWasConfirmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	tools := &tools{run: runner.run, root: dir}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		InputResponses: mcp.InputResponseMap{confirmation: &mcp.ElicitResult{Action: "accept"}},
		RequestState:   "a confirmation of another import",
	}}
	in := ImportInput{Scope: Scope{Provider: "aws", Resources: []string{"vpc"}}, Selection: path, Output: filepath.Join(dir, "generated")}

	res, _, err := tools.importSelection(t.Context(), req, in)

	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want an error result, got %+v, %v", res, err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("imported without a matching confirmation: %v", runner.runs)
	}
}

// The tools read and write files under the root only: an agent may not
// write a selection file or configuration, or read a report, anywhere
// else on the machine.
func TestFilesStayUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRunner{}
	tools := &tools{run: runner.run, root: root}
	scope := Scope{Provider: "aws", Resources: []string{"vpc"}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	for name, path := range map[string]string{
		"outside":        filepath.Join(outside, "selection.yaml"),
		"up from inside": filepath.Join(root, "..", "elsewhere", "selection.yaml"),
	} {
		if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: path}); err == nil || !strings.Contains(err.Error(), "must be under") {
			t.Errorf("discover %s: got %v, want the path rejected", name, err)
		}
	}
	inside := filepath.Join(root, "selection.yaml")
	if err := writeSelection(inside); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tools.importSelection(t.Context(), req, ImportInput{Scope: scope, Selection: inside, Output: filepath.Join(outside, "generated")}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("import outside: got %v, want the output rejected", err)
	}
	if _, _, err := tools.importSelection(t.Context(), req, ImportInput{Scope: scope, Selection: filepath.Join(outside, "selection.yaml"), Output: filepath.Join(root, "generated")}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("import from outside: got %v, want the selection rejected", err)
	}
	if _, _, err := tools.report(t.Context(), req, ReportInput{Output: outside}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("report outside: got %v, want the output rejected", err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran with files outside the root: %v", runner.runs)
	}

	// Under the root, relative to it or not, is fine.
	if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: filepath.Join(root, "sub", "..", "selection.yaml")}); err != nil {
		t.Errorf("discover inside: %v", err)
	}
	if len(runner.runs) != 1 {
		t.Errorf("runs: %v", runner.runs)
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
)

// fakeRunner records the arguments of each run, and writes a selection
// file for discover and prints a report for import.
type fakeRunner struct {
	runs [][]string
}

func (f *fakeRunner) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	f.runs = append(f.runs, args)
	switch args[0] {
	case "discover":
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

// connect returns a client session with the server; elicit answers the
// server's confirmation requests, or nil for a client that can't.
func connect(t *testing.T, runner *fakeRunner, elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := New("test", runner.run).Connect(t.Context(), serverTransport, nil); err != nil {
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
	session := connect(t, runner, nil)
	path := filepath.Join(t.TempDir(), "selection.yaml")

	res, text := call(t, session, "discover", map[string]any{"provider": "aws", "resources": []string{"vpc", "subnet"}, "regions": []string{"eu-west-2"}, "selection": path})

	if res.IsError || !strings.Contains(text, "lists 3 resources: 2 included, 1 excluded") {
		t.Errorf("result: %s", text)
	}
	want := []string{"discover", "aws", "--resources=vpc,subnet", "--regions=eu-west-2", "--engine=terraform", "--selection=" + path}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
	summary, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(summary), "aws_vpc vpc-default: the default VPC") {
		t.Errorf("summary misses the exclusion: %s", summary)
	}
}

func importArgs(selectionPath, output string) map[string]any {
	return map[string]any{"provider": "aws", "resources": []string{"vpc"}, "selection": selectionPath, "output": output}
}

func TestImportAfterTheUserConfirms(t *testing.T) {
	runner := &fakeRunner{}
	var asked string
	session := connect(t, runner, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = req.Params.Message
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "generated")

	res, text := call(t, session, "import", importArgs(path, out))

	if res.IsError || !strings.Contains(text, "Imported 2 of 3 resources") {
		t.Errorf("result: %s", text)
	}
	if !strings.Contains(asked, "Import the 2 resources") || !strings.Contains(asked, "never applies") {
		t.Errorf("confirmation: %q", asked)
	}
	want := []string{"import", "aws", "--resources=vpc", "--engine=terraform", "--selection=" + path, "--path-output=" + out, "--output=json"}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
}

func TestImportNeedsTheUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
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
			session := connect(t, runner, tc.elicit)

			res, text := call(t, session, "import", importArgs(path, "generated"))

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

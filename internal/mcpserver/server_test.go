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

	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
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
		r := report.Report{Totals: report.CoverageTotal{Discovered: 3, Imported: 2, Excluded: 1}}
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

	res, text := call(t, session, "discover", map[string]any{"provider": "aws", "services": []string{"vpc", "subnet"}, "regions": []string{"eu-west-2"}, "selection": path})

	if res.IsError || !strings.Contains(text, "lists 3 resources: 2 included, 1 excluded") {
		t.Errorf("result: %s", text)
	}
	want := []string{"discover", "aws", "--services=vpc,subnet", "--regions=eu-west-2", "--pick=false", "--selection=" + path}
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

	res, text := call(t, session, "discover", map[string]any{"provider": "aws", "services": []string{"vpc"}, "selection": path})

	if !res.IsError || !strings.Contains(text, "no credentials found") || strings.Contains(text, "lists 3 resources") {
		t.Errorf("want an error result with the command's output, got %s", text)
	}
}

func importArgs(selectionPath, output string) map[string]any {
	return map[string]any{"provider": "aws", "services": []string{"vpc"}, "regions": []string{"eu-west-2"}, "profile": "prod", "selection": selectionPath, "out": output}
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
	for _, want := range []string{"Import the 2 resources", "never applies", "with profile prod", "eu-west-2", "vpc"} {
		if !strings.Contains(asked, want) {
			t.Errorf("confirmation %q misses %q", asked, want)
		}
	}
	want := []string{"import", "aws", "--services=vpc", "--regions=eu-west-2", "--profile=prod", "--engine=terraform", "--selection=" + path, "--out=" + out, "--format=json"}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
}

// An incremental import adds to the roots the output directory has, and
// the user is told so.
func TestImportIncremental(t *testing.T) {
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
	args := importArgs(path, filepath.Join(dir, "generated"))
	args["incremental"] = true

	if res, text := call(t, session, "import", args); res.IsError {
		t.Fatalf("result: %s", text)
	}
	if !strings.Contains(asked, "don't have yet") || !strings.Contains(asked, "stays as it is") {
		t.Errorf("confirmation %q doesn't say the import adds to the roots", asked)
	}
	if len(runner.runs) != 1 || !slices.Contains(runner.runs[0], "--incremental") {
		t.Errorf("runs: %v, want --incremental", runner.runs)
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
		"a client that can't ask": {nil, "infraharvest import aws --services=vpc"},
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
	for _, provider := range []string{"--help", "aws --all", "", "AWS", "gcp"} {
		if _, err := scopeArgs("import", Scope{Provider: provider, Services: []string{"vpc"}}); err == nil {
			t.Errorf("provider %q accepted", provider)
		}
	}
	for _, tc := range []struct {
		scope Scope
		want  []string
	}{
		{Scope{Provider: "aws", Services: []string{"--all"}, Profile: "x --y"}, []string{"import", "aws", "--services=--all", "--profile=x --y"}},
		{Scope{Provider: "google", Services: []string{"iam"}, Organization: "--folders=1"}, []string{"import", "google", "--services=iam", "--organization=--folders=1"}},
		{Scope{Provider: "azure", Services: []string{"resource_graph"}, ResourceGroup: "-g x"}, []string{"import", "azure", "--services=resource_graph", "--resource-group=-g x"}},
	} {
		args, err := scopeArgs("import", tc.scope)
		if err != nil || !slices.Equal(args, tc.want) {
			t.Errorf("scopeArgs(%+v): got %v, %v, want %v", tc.scope, args, err, tc.want)
		}
	}
}

// Each provider's scope fields become the flags its command has.
func TestScopeArgsByProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		scope Scope
		want  []string
	}{
		"aws": {
			Scope{Provider: "aws", Services: []string{"vpc", "s3"}, Regions: []string{"eu-west-1", "us-east-1"}, Profile: "prod", Accounts: []string{"111111111111", "222222222222"}},
			[]string{"discover", "aws", "--services=vpc,s3", "--regions=eu-west-1,us-east-1", "--profile=prod", "--accounts=111111111111,222222222222"},
		},
		"google projects": {
			Scope{Provider: "google", Services: []string{"iam"}, Projects: []string{"one", "two"}, Regions: []string{"global", "europe-west1"}},
			[]string{"discover", "google", "--services=iam", "--regions=global,europe-west1", "--projects=one,two"},
		},
		"google organization and folders": {
			Scope{Provider: "google", Services: []string{"iam"}, Organization: "123", Folders: []string{"456", "789"}},
			[]string{"discover", "google", "--services=iam", "--organization=123", "--folders=456,789"},
		},
		"azure from the environment": {
			Scope{Provider: "azure", Services: []string{"resource_graph"}},
			[]string{"discover", "azure", "--services=resource_graph"},
		},
		"azure": {
			Scope{Provider: "azure", Services: []string{"resource_graph"}, Subscriptions: []string{"sub-1", "sub-2"}, ManagementGroup: "mg", ResourceGroup: "rg"},
			[]string{"discover", "azure", "--services=resource_graph", "--subscriptions=sub-1,sub-2", "--management-group=mg", "--resource-group=rg"},
		},
	} {
		args, err := scopeArgs("discover", tc.scope)
		if err != nil || !slices.Equal(args, tc.want) {
			t.Errorf("%s: scopeArgs: got %v, %v, want %v", name, args, err, tc.want)
		}
	}
}

// A field the provider's command has no flag for is rejected with a
// message naming it. Before the fix, Azure scopes passed --regions and
// --profile, which the azure command fails on as unknown flags, and Google
// scopes had no way to name a project, so the google command always
// failed.
func TestScopeArgsRejectsWhatTheProviderDoesNotTake(t *testing.T) {
	for name, tc := range map[string]struct {
		scope Scope
		want  []string
	}{
		"azure regions and profile": {
			Scope{Provider: "azure", Services: []string{"resource_graph"}, Regions: []string{"westeurope"}, Profile: "prod"},
			[]string{"azure doesn't take regions", "azure doesn't take profile"},
		},
		"aws projects and resource group": {
			Scope{Provider: "aws", Services: []string{"vpc"}, Projects: []string{"one"}, ResourceGroup: "rg"},
			[]string{"aws doesn't take projects", "aws doesn't take resource_group"},
		},
		"google subscriptions": {
			Scope{Provider: "google", Services: []string{"iam"}, Projects: []string{"one"}, Subscriptions: []string{"sub-1"}},
			[]string{"google doesn't take subscriptions, which is for azure only"},
		},
		"google profile and accounts": {
			Scope{Provider: "google", Services: []string{"iam"}, Folders: []string{"456"}, Profile: "prod", Accounts: []string{"111111111111"}},
			[]string{"google doesn't take profile", "google doesn't take accounts"},
		},
		"google without projects": {
			Scope{Provider: "google", Services: []string{"iam"}, Regions: []string{"global"}},
			[]string{"google needs projects, organization or folders"},
		},
		"no services": {
			Scope{Provider: "google", Projects: []string{"one"}},
			[]string{"services is required"},
		},
	} {
		args, err := scopeArgs("discover", tc.scope)
		if err == nil {
			t.Errorf("%s: scopeArgs accepted it: %v", name, args)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q misses %q", name, err, want)
			}
		}
	}
}

// The user confirms an import knowing what it reads, in the provider's
// own terms.
func TestScopeText(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		want  string
	}{
		{Scope{Provider: "aws", Services: []string{"vpc"}}, "the aws account (vpc)"},
		{Scope{Provider: "aws", Services: []string{"vpc", "s3"}, Profile: "prod", Regions: []string{"eu-west-2"}}, "the aws account with profile prod in eu-west-2 (vpc, s3)"},
		{Scope{Provider: "aws", Services: []string{"vpc"}, Accounts: []string{"111", "222"}}, "the aws accounts 111, 222 (vpc)"},
		{Scope{Provider: "google", Services: []string{"iam"}, Projects: []string{"one", "two"}}, "the google projects one, two in global (iam)"},
		{Scope{Provider: "google", Services: []string{"iam"}, Organization: "123", Folders: []string{"456"}, Regions: []string{"europe-west1"}}, "every active google project of organization 123 and of folders 456 in europe-west1 (iam)"},
		{Scope{Provider: "azure", Services: []string{"resource_graph"}}, "the azure subscription of ARM_SUBSCRIPTION_ID (resource_graph)"},
		{Scope{Provider: "azure", Services: []string{"resource_graph"}, Subscriptions: []string{"sub-1"}, ManagementGroup: "mg", ResourceGroup: "rg"}, "resource group rg of the azure subscriptions sub-1 and every enabled subscription under management group mg (resource_graph)"},
	} {
		if got := scopeText(tc.scope); got != tc.want {
			t.Errorf("scopeText(%+v): got %q, want %q", tc.scope, got, tc.want)
		}
	}
}

// Through the tools: a Google scope reaches the google command with its
// projects, and a rejected scope runs nothing.
func TestDiscoverScopes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	runner := &fakeRunner{}
	session := connect(t, runner, dir, nil)
	res, text := call(t, session, "discover", map[string]any{"provider": "google", "services": []string{"iam"}, "projects": []string{"my-project"}, "selection": path})
	if res.IsError {
		t.Errorf("result: %s", text)
	}
	want := []string{"discover", "google", "--services=iam", "--projects=my-project", "--pick=false", "--selection=" + path}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}

	runner = &fakeRunner{}
	tools := &tools{run: runner.run, root: dir}
	for name, scope := range map[string]Scope{
		"azure regions":           {Provider: "azure", Services: []string{"resource_graph"}, Regions: []string{"westeurope"}},
		"google without projects": {Provider: "google", Services: []string{"iam"}},
	} {
		if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: path}); err == nil {
			t.Errorf("discover %s: accepted", name)
		}
		if _, _, err := tools.importSelection(t.Context(), req, ImportInput{Scope: scope, Selection: path, Out: filepath.Join(dir, "generated")}); err == nil {
			t.Errorf("import %s: accepted", name)
		}
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran a rejected scope: %v", runner.runs)
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
	in := ImportInput{Provider: "aws", Services: []string{"vpc"}, Selection: path, Out: filepath.Join(dir, "generated")}

	res, _, err := tools.importSelection(t.Context(), req, in)

	if err != nil || res == nil || !res.IsError {
		t.Fatalf("want an error result, got %+v, %v", res, err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("imported without a matching confirmation: %v", runner.runs)
	}
}

// A selection file changed after the user was asked isn't imported, even
// when it includes and excludes as many resources as before.
func TestImportChecksTheConfirmedSelection(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		swapped := &selection.File{Version: selection.Version, Resources: []selection.Resource{
			{Type: "aws_subnet", ID: "subnet-1", Include: true},
			{Type: "aws_vpc", ID: "vpc-default", Include: true},
			{Type: "aws_vpc", ID: "vpc-1", Reason: "excluded after the user was asked"},
		}}
		return &mcp.ElicitResult{Action: "accept"}, swapped.Save(path)
	})
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}

	res, text := call(t, session, "import", importArgs(path, filepath.Join(dir, "generated")))

	if !res.IsError || !strings.Contains(text, "didn't confirm") {
		t.Errorf("want the import refused, got %s", text)
	}
	if len(runner.runs) != 0 {
		t.Errorf("imported a selection the user didn't confirm: %v", runner.runs)
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
	scope := Scope{Provider: "aws", Services: []string{"vpc"}}
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
	if _, _, err := tools.importSelection(t.Context(), req, ImportInput{Scope: scope, Selection: inside, Out: filepath.Join(outside, "generated")}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("import outside: got %v, want the output rejected", err)
	}
	if _, _, err := tools.importSelection(t.Context(), req, ImportInput{Scope: scope, Selection: filepath.Join(outside, "selection.yaml"), Out: filepath.Join(root, "generated")}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("import from outside: got %v, want the selection rejected", err)
	}
	if _, _, err := tools.report(t.Context(), req, ReportInput{Out: outside}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("report outside: got %v, want the output rejected", err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran with files outside the root: %v", runner.runs)
	}

	// A symbolic link under the root doesn't lead out of it, nor does a
	// ".." after one.
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Logf("no symbolic links here: %v", err)
	} else {
		for name, path := range map[string]string{
			"through a link":   filepath.Join(link, "selection.yaml"),
			"up from a link":   link + string(filepath.Separator) + ".." + string(filepath.Separator) + "selection.yaml",
			"into a link":      link,
			"a new file there": filepath.Join(link, "new", "selection.yaml"),
		} {
			if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: path}); err == nil || !strings.Contains(err.Error(), "must be under") {
				t.Errorf("discover %s: got %v, want the path rejected", name, err)
			}
		}
		if len(runner.runs) != 0 {
			t.Errorf("ran with files outside the root: %v", runner.runs)
		}
	}

	// Under the root, relative to it or not, is fine.
	if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: filepath.Join(root, "sub", "..", "selection.yaml")}); err != nil {
		t.Errorf("discover inside: %v", err)
	}
	if len(runner.runs) != 1 {
		t.Errorf("runs: %v", runner.runs)
	}
}

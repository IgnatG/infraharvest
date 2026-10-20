// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
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
// file for discover, prints a report for import, and checks for drift.
// With discoverExit set, discover fails with that code and discoverStderr
// instead.
type fakeRunner struct {
	runs           [][]string
	discoverExit   int
	discoverStderr string
	// selections are the contents of the selection files imports read.
	selections []string
	// driftExit and driftStderr make drift fail with them, printing
	// nothing; decisions are the contents of the decisions files drift
	// read (see fakeDrift).
	driftExit   int
	driftStderr string
	decisions   []string
	// importReport, when set, is the report import prints instead of the
	// default one.
	importReport *report.Report
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
		for _, a := range args {
			if path, ok := strings.CutPrefix(a, "--selection="); ok {
				content, err := os.ReadFile(path)
				if err != nil {
					return nil, nil, 2, err
				}
				f.selections = append(f.selections, string(content))
			}
		}
		r := report.Report{Totals: report.CoverageTotal{Discovered: 3, Imported: 2, Excluded: 1}}
		if f.importReport != nil {
			r = *f.importReport
		}
		out, err := json.Marshal(r)
		return out, []byte("imported 2 of 3 resources\n"), 0, err
	case "drift":
		return f.fakeDrift(args)
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
	want := []string{"import", "aws", "--services=vpc", "--regions=eu-west-2", "--profile=prod", "--engine=terraform", "--selection=", "--out=" + out, "--format=json"}
	if len(runner.runs) != 1 {
		t.Fatalf("runs: %v, want one", runner.runs)
	}
	// The import reads the confirmed content from a private copy, removed
	// once it is done.
	got := slices.Clone(runner.runs[0])
	copied := ""
	for i, a := range got {
		if p, ok := strings.CutPrefix(a, "--selection="); ok {
			copied, got[i] = p, "--selection="
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
	if copied == path || copied == "" {
		t.Errorf("the import read %q, not a private copy of %s", copied, path)
	}
	if _, err := os.Stat(copied); err == nil {
		t.Errorf("the copy %s is left behind", copied)
	}
	if original, err := os.ReadFile(path); err != nil || len(runner.selections) != 1 || runner.selections[0] != string(original) {
		t.Errorf("the import read %q, want the confirmed content %q (%v)", runner.selections, original, err)
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

// An import with regenerate writes the roots the output directory has
// again, and the user is told so; it doesn't go with incremental.
func TestImportRegenerate(t *testing.T) {
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
	args["regenerate"] = true

	if res, text := call(t, session, "import", args); res.IsError {
		t.Fatalf("result: %s", text)
	}
	if !strings.Contains(asked, "again, from scratch") || !strings.Contains(asked, "left as it was") {
		t.Errorf("confirmation %q doesn't say the import writes the roots again", asked)
	}
	if len(runner.runs) != 1 || !slices.Contains(runner.runs[0], "--regenerate") || slices.Contains(runner.runs[0], "--incremental") {
		t.Errorf("runs: %v, want --regenerate", runner.runs)
	}

	tools := &tools{run: runner.run, root: dir}
	in := ImportInput{Scope: Scope{Provider: "aws", Services: []string{"vpc"}}, Selection: path, Out: filepath.Join(dir, "generated"), Incremental: true, Regenerate: true}
	if _, _, err := tools.importSelection(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}, in); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("with incremental too: want an error, got %v", err)
	}
	if len(runner.runs) != 1 {
		t.Errorf("imported with incremental and regenerate: %v", runner.runs)
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

// Clients auto-approve read-only tools and retry idempotent ones: discover
// writes the selection file, the same import again isn't the same, and
// drift_accept writes into the code what drift only reports.
func TestToolAnnotations(t *testing.T) {
	session := connect(t, &fakeRunner{}, t.TempDir(), nil)
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 5 {
		t.Fatalf("got %d tools, want 5", len(res.Tools))
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		switch tool.Name {
		case "report", "drift":
			if !a.ReadOnlyHint {
				t.Errorf("%s isn't read-only", tool.Name)
			}
		default:
			if a.ReadOnlyHint || a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint {
				t.Errorf("%s: annotations %+v, want neither read-only nor idempotent, and not destructive", tool.Name, a)
			}
		}
	}
}

// A symbolic link a cloned repository plants under the output directory
// doesn't take the report tool, or an import, out of the root.
func TestLinksUnderTheOutputStayUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	for _, dir := range []string{filepath.Join(root, "linked-report", report.Dir), filepath.Join(root, "generated"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "credentials")
	if err := os.WriteFile(secret, []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "linked-report", report.Dir, "report.md")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "linked-parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-parent", report.Dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "generated", "aws")); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	tools := &tools{run: runner.run, root: root}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	for _, out := range []string{"linked-report", "linked-parent"} {
		if res, _, err := tools.report(t.Context(), req, ReportInput{Out: filepath.Join(root, out)}); err == nil || !strings.Contains(err.Error(), "must be under") {
			t.Errorf("report %s: got %+v, %v, want the link rejected", out, res, err)
		}
	}
	selectionPath := filepath.Join(root, "selection.yaml")
	if err := writeSelection(selectionPath); err != nil {
		t.Fatal(err)
	}
	in := ImportInput{Scope: Scope{Provider: "aws", Services: []string{"vpc"}}, Selection: selectionPath, Out: filepath.Join(root, "generated")}
	if _, _, err := tools.importSelection(t.Context(), req, in); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("import: got %v, want the link under the output rejected", err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran with a link out of the root: %v", runner.runs)
	}
}

// An answer counts only to a confirmation request the server made, and
// only once: a client can't skip asking by computing the state, nor run
// one confirmed import twice.
func TestImportRedeemsEachConfirmationOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "generated")
	scope := Scope{Provider: "aws", Services: []string{"vpc"}}
	args, err := scopeArgs("import", scope)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, "--engine=terraform", "--selection="+path, "--out="+out, "--format=json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := confirmationState(args, content)
	runner := &fakeRunner{}
	tools := &tools{run: runner.run, root: dir}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		InputResponses: mcp.InputResponseMap{confirmation: &mcp.ElicitResult{Action: "accept"}},
		RequestState:   state,
	}}
	in := ImportInput{Scope: scope, Selection: path, Out: out}

	if res, _, err := tools.importSelection(t.Context(), req, in); err != nil || res == nil || !res.IsError {
		t.Errorf("an answer never asked for: got %+v, %v, want the import refused", res, err)
	}
	if len(runner.runs) != 0 {
		t.Fatalf("imported without asking: %v", runner.runs)
	}

	tools.issue(state)
	if res, _, err := tools.importSelection(t.Context(), req, in); err != nil || res == nil || res.IsError {
		t.Errorf("a confirmed import: got %+v, %v", res, err)
	}
	if res, _, err := tools.importSelection(t.Context(), req, in); err != nil || res == nil || !res.IsError {
		t.Errorf("the same answer again: got %+v, %v, want the import refused", res, err)
	}
	if len(runner.runs) != 1 {
		t.Errorf("runs: %v, want one", runner.runs)
	}
}

// The command a user is told to run reads back as the same arguments, and
// what the agent sends can't make the confirmation say something else.
func TestValuesShownToTheUser(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import", "aws", "--services=vpc,s3", "--out=gen"}, "import aws --services=vpc,s3 --out=gen"},
		{[]string{"--selection=my dir/sel.yaml"}, "'--selection=my dir/sel.yaml'"},
		{[]string{"--out=gen;curl x|sh"}, "'--out=gen;curl x|sh'"},
		{[]string{"--profile=it's"}, `'--profile=it'"'"'s'`},
	} {
		if got := shellJoin(tc.args); got != tc.want {
			t.Errorf("shellJoin(%q): got %s, want %s", tc.args, got, tc.want)
		}
	}

	for name, scope := range map[string]Scope{
		"a service with a space":    {Provider: "aws", Services: []string{"vpc; rm"}},
		"a region with a newline":   {Provider: "aws", Services: []string{"vpc"}, Regions: []string{"eu-west-2\nand nothing else"}},
		"a profile with a newline":  {Provider: "aws", Services: []string{"vpc"}, Profile: "prod\nImport nothing"},
		"a project with a carriage": {Provider: "google", Services: []string{"iam"}, Projects: []string{"one\r"}},
	} {
		if _, err := scopeArgs("import", scope); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	dir := t.TempDir()
	tools := &tools{run: (&fakeRunner{}).run, root: dir}
	if _, err := tools.within("out", filepath.Join(dir, "gen\nInto nothing")); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Errorf("a path with a newline: got %v", err)
	}
}

// The confirmation quotes the paths the agent chose.
func TestConfirmationQuotesPaths(t *testing.T) {
	runner := &fakeRunner{}
	var asked string
	dir := t.TempDir()
	session := connect(t, runner, dir, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = req.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	})
	path := filepath.Join(dir, "my selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gen")
	call(t, session, "import", importArgs(path, out))
	for _, want := range []string{fmt.Sprintf("%q includes", path), fmt.Sprintf("in %q?", out)} {
		if !strings.Contains(asked, want) {
			t.Errorf("confirmation %q misses %s", asked, want)
		}
	}
}

// A host that starts the server in / or the home directory doesn't give
// the tools the whole machine without --root saying so.
func TestCheckDefaultRoot(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	volumeRoot := filepath.VolumeName(home) + string(filepath.Separator)
	for dir, ok := range map[string]bool{
		project:    true,
		home:       false,
		volumeRoot: false,
	} {
		if err := CheckDefaultRoot(dir, home); (err == nil) != ok {
			t.Errorf("CheckDefaultRoot(%s): got %v, want allowed %v", dir, err, ok)
		}
	}
	if err := CheckDefaultRoot(home, ""); err != nil {
		t.Errorf("without a home directory: %v", err)
	}
}

// A relative path is relative to the root, wherever the server runs: the
// child gets it, and the report is read from it, joined onto the root.
func TestRelativePathsAreUnderRoot(t *testing.T) {
	root := t.TempDir()
	t.Chdir(t.TempDir())
	runner := &fakeRunner{}
	tools := &tools{run: runner.run, root: root}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	scope := Scope{Provider: "aws", Services: []string{"vpc"}}

	if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: "selection.yaml"}); err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := "--selection=" + filepath.Join(root, "selection.yaml")
	if len(runner.runs) != 1 || runner.runs[0][len(runner.runs[0])-1] != want {
		t.Errorf("runs: %v, want the last argument %s", runner.runs, want)
	}
	if _, err := os.Stat(filepath.Join(root, "selection.yaml")); err != nil {
		t.Errorf("the selection file isn't under the root: %v", err)
	}
	if _, _, err := tools.discover(t.Context(), req, DiscoverInput{Scope: scope, Selection: filepath.Join("..", "selection.yaml")}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("a relative path up from the root: got %v, want it rejected", err)
	}
	if _, _, err := tools.report(t.Context(), req, ReportInput{Out: "generated"}); err == nil || !strings.Contains(err.Error(), filepath.Join(root, "generated")) {
		t.Errorf("report: got %v, want it read under the root", err)
	}
}

// A summary names the first exclusions and counts the rest, which the
// selection file has: an organisation can exclude tens of thousands.
func TestSummarizeBoundsTheExclusions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	f := &selection.File{Version: selection.Version, Resources: []selection.Resource{{Type: "aws_vpc", ID: "vpc-kept", Include: true}}}
	for i := range maxExclusions + 7 {
		f.Resources = append(f.Resources, selection.Resource{Type: "aws_vpc", ID: fmt.Sprintf("vpc-%03d", i), Reason: "the default VPC"})
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	summary, err := summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Included != 1 || summary.Excluded != maxExclusions+7 {
		t.Errorf("counts: %+v", summary)
	}
	if len(summary.Exclusions) != maxExclusions || summary.MoreExclusions != 7 {
		t.Errorf("got %d exclusions and %d more, want %d and 7", len(summary.Exclusions), summary.MoreExclusions, maxExclusions)
	}
	if summary.Exclusions[0] != "aws_vpc vpc-000: the default VPC" {
		t.Errorf("first exclusion: %q", summary.Exclusions[0])
	}
}

// The report tool returns the start of a long report, cut after a whole
// line, and says how much it left out and where the full report is.
func TestReportBoundsALongReport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "generated", report.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 99) + "\n"
	content := strings.Repeat(line, maxReport/len(line)+10)
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &tools{run: (&fakeRunner{}).run, root: root}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	res, _, err := tools.report(t.Context(), req, ReportInput{Out: filepath.Join(root, "generated")})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	head, note, ok := strings.Cut(text, "\n\n[")
	if !ok {
		t.Fatalf("no note on the cut report: %q", text[len(text)-200:])
	}
	// The kept part ends with its last line's newline, which Cut leaves on
	// head.
	if len(head) > maxReport || maxReport-len(head) >= len(line) || !strings.HasPrefix(content, head) || len(head)%len(line) != 0 {
		t.Errorf("want whole lines up to %d bytes, got %d bytes", maxReport, len(head))
	}
	left := len(content) - len(head)
	if want := fmt.Sprintf("%d of the report's %d bytes are left out here: the full report is %s]", left, len(content), path); note != want {
		t.Errorf("note: %q, want %q", note, want)
	}
}

// A short report comes back whole.
func TestReportReturnsAShortReportWhole(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "generated", report.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &tools{run: (&fakeRunner{}).run, root: root}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	res, _, err := tools.report(t.Context(), req, ReportInput{Out: filepath.Join(root, "generated")})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; text != "# Report\n" {
		t.Errorf("got %q", text)
	}
}

// Standard error comes back as its last lines, each cut to maxLine bytes
// on a whole character.
func TestTailBoundsLines(t *testing.T) {
	var lines []string
	for i := range 25 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines = append(lines, strings.Repeat("é", maxLine))
	got := strings.Split(tail([]byte(strings.Join(lines, "\n")+"\n")), "\n")
	if len(got) != 20 || got[0] != "line 6" {
		t.Fatalf("got %d lines starting %q, want 20 starting \"line 6\"", len(got), got[0])
	}
	long, ok := strings.CutSuffix(got[19], " [cut]")
	if !ok || len(long) > maxLine || long != strings.Repeat("é", maxLine/2) {
		t.Errorf("long line: %d bytes, cut %v", len(long), ok)
	}
}

// A discover a signal stopped may not have written the selection file:
// one that is there may be an earlier run's, so it isn't read.
func TestDiscoverStopped(t *testing.T) {
	for _, code := range []int{exitInterrupted, exitTerminated, -1} {
		runner := &fakeRunner{discoverExit: code, discoverStderr: "interrupted\n"}
		dir := t.TempDir()
		session := connect(t, runner, dir, nil)
		path := filepath.Join(dir, "selection.yaml")
		if err := writeSelection(path); err != nil {
			t.Fatal(err)
		}

		res, text := call(t, session, "discover", map[string]any{"provider": "aws", "services": []string{"vpc"}, "selection": path})

		if !res.IsError || !strings.Contains(text, "discover was stopped") || strings.Contains(text, "lists 3 resources") {
			t.Errorf("exit code %d: want an error result saying discover was stopped, got %s", code, text)
		}
	}
}

// The roots an earlier import generated that keep their directories, though
// the configuration file's environments would lay them out under envs/ now,
// are in the result, and its text says what to do.
func TestImportReportsKeptRoots(t *testing.T) {
	kept := []report.KeptRoot{{Path: "aws/123456789012/eu-west-2/vpc", Environments: []string{"dev", "prod"}, Scope: "aws/123456789012"}}
	r := report.Report{Coverage: report.Coverage{Totals: report.CoverageTotal{Discovered: 2, Imported: 2}, KeptRoots: kept}}
	runner := &fakeRunner{importReport: &r}
	dir := t.TempDir()
	session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}

	res, text := call(t, session, "import", importArgs(path, filepath.Join(dir, "generated")))
	if res.IsError {
		t.Fatalf("result: %s", text)
	}
	if !strings.Contains(text, "1 roots an earlier import generated keep their directories") || !strings.Contains(text, "kept_roots") {
		t.Errorf("text %q doesn't say which roots kept their directories", text)
	}
	content, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got ImportResult
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.KeptRoots) != 1 || got.KeptRoots[0].Path != kept[0].Path || !slices.Equal(got.KeptRoots[0].Environments, kept[0].Environments) || got.KeptRoots[0].Scope != kept[0].Scope {
		t.Errorf("kept_roots: %+v, want %+v", got.KeptRoots, kept)
	}
}

// kind drift reads drift.md; without kind, the import's report.md, or
// drift.md when the output directory has only that.
func TestReportReadsTheDriftReport(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "generated")
	dir := filepath.Join(out, report.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, report.DriftMarkdownFile), []byte("# Drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &tools{run: (&fakeRunner{}).run, root: root}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	read := func(kind string) (string, error) {
		res, _, err := tools.report(t.Context(), req, ReportInput{Out: out, Kind: kind})
		if err != nil {
			return "", err
		}
		return res.Content[0].(*mcp.TextContent).Text, nil
	}

	for _, kind := range []string{"", "drift"} {
		if text, err := read(kind); err != nil || text != "# Drift\n" {
			t.Errorf("kind %q with only drift.md: got %q, %v", kind, text, err)
		}
	}
	if _, err := read("import"); err == nil || !strings.Contains(err.Error(), "no report") {
		t.Errorf("kind import with only drift.md: got %v, want no report", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]string{"": "# Report\n", "import": "# Report\n", "drift": "# Drift\n"} {
		if text, err := read(kind); err != nil || text != want {
			t.Errorf("kind %q with both: got %q, %v, want %q", kind, text, err, want)
		}
	}
	if _, err := read("verify"); err == nil || !strings.Contains(err.Error(), "kind must be") {
		t.Errorf("kind verify: got %v, want it rejected", err)
	}
}

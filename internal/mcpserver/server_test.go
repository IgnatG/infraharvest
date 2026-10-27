// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/harvest"
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
	// checked are the arguments of each check; checkErr is the error each
	// returns.
	checked  [][]string
	checkErr error
	// configs are the contents of the configuration files each run and
	// check read.
	configs []string
	// rootsDigests are the --roots-digest of each drift run, which runs
	// records without it.
	rootsDigests []string
}

func (f *fakeRunner) check(_ context.Context, args []string) error {
	f.readConfig(args)
	f.checked = append(f.checked, args)
	return f.checkErr
}

// compiled are the providers compiled into the tests, as internal/cli passes
// them to the server.
var compiled = Providers{Profiles: providers.Profiles(), Sources: providers.Sources(), Check: providers.Check}

// builtIn reports whether the tests are built with each of names: a
// build with -tags minimal has only some providers, and its tests leave
// out what takes the others, as internal/cli's do.
func builtIn(names ...string) bool {
	for _, name := range names {
		if !slices.Contains(providers.Names(), name) {
			return false
		}
	}
	return true
}

// needProviders skips t unless the tests are built with each of names
// (see builtIn).
func needProviders(t *testing.T, names ...string) {
	t.Helper()
	if !builtIn(names...) {
		t.Skipf("built without %s (-tags minimal)", strings.Join(names, " or "))
	}
}

// newTools returns the tools, whose files stay under root, running and
// checking with runner.
func newTools(runner *fakeRunner, root string) *tools {
	return &tools{run: runner.run, check: runner.check, providers: compiled, root: root}
}

func (f *fakeRunner) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	f.readConfig(args)
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool {
		digest, ok := strings.CutPrefix(a, "--roots-digest=")
		if ok {
			f.rootsDigests = append(f.rootsDigests, digest)
		}
		return ok
	})
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
	if _, err := New("test", root, runner.run, runner.check, compiled).Connect(t.Context(), serverTransport, nil); err != nil {
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
	needProviders(t, "aws")
	runner := &fakeRunner{}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)
	path := filepath.Join(dir, "selection.yaml")

	res, text := call(t, session, "discover", map[string]any{"provider": "aws", "services": []string{"vpc", "subnet"}, "regions": []string{"eu-west-2"}, "selection": path})

	if res.IsError || !strings.Contains(text, "lists 3 resources: 2 included, 1 excluded") {
		t.Errorf("result: %s", text)
	}
	want := []string{"discover", "aws", "--services=vpc,subnet", "--regions=eu-west-2", "--pick=false", "--out=" + filepath.Join(dir, "infraharvest-out"), "--selection=" + path}
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
	needProviders(t, "aws")
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
	needProviders(t, "aws")
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
	want := []string{"import", "aws", "--services=vpc", "--regions=eu-west-2", "--profile=prod", "--selection=", "--out=" + out, "--format=json"}
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

// An import the command's checks refuse fails before the user is asked,
// with the command's error, and runs nothing.
func TestImportChecksBeforeAsking(t *testing.T) {
	needProviders(t, "google")
	runner := &fakeRunner{checkErr: errors.New("say which projects to import: --projects, --organization or --folders")}
	asked := false
	dir := t.TempDir()
	session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = true
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "generated")

	res, text := call(t, session, "import", map[string]any{"provider": "google", "services": []string{"iam"}, "selection": path, "out": out})

	if !res.IsError || !strings.Contains(text, "say which projects") {
		t.Errorf("want the command's error, got %s", text)
	}
	if asked || len(runner.runs) != 0 {
		t.Errorf("asked %v, runs %v: want neither", asked, runner.runs)
	}
	want := []string{"import", "google", "--services=iam", "--selection=" + path, "--out=" + out, "--format=json"}
	if len(runner.checked) != 1 || !slices.Equal(runner.checked[0], want) {
		t.Errorf("checked %v, want %v", runner.checked, want)
	}
}

// An incremental import adds to the roots the output directory has, and
// the user is told so.
func TestImportIncremental(t *testing.T) {
	needProviders(t, "aws")
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
// again, and the user is told so.
func TestImportRegenerate(t *testing.T) {
	needProviders(t, "aws")
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
}

func TestImportNeedsTheUser(t *testing.T) {
	needProviders(t, "aws")
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

// A provider the binary doesn't have is rejected: it is the one argument
// that isn't a --flag=value. Every other value stays in its flag.
func TestCommandArgsKeepValuesOutOfFlags(t *testing.T) {
	for _, provider := range []string{"--help", "aws --all", "", "AWS", "gcp"} {
		scope := Scope{Provider: provider, Services: []string{"vpc"}}
		if _, err := compiled.commandArgs("import", scope, scope); err == nil || !strings.Contains(err.Error(), "unknown provider") {
			t.Errorf("provider %q: got %v, want it rejected", provider, err)
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
		if !builtIn(tc.scope.Provider) {
			continue
		}
		args, err := compiled.commandArgs("import", tc.scope, tc.scope)
		if err != nil || !slices.Equal(args, tc.want) {
			t.Errorf("commandArgs(%+v): got %v, %v, want %v", tc.scope, args, err, tc.want)
		}
	}
}

// Each scope field becomes the flag its tag names; what a provider's
// command makes of them is for the command to check.
func TestCommandArgsByProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		scope Scope
		want  []string
	}{
		"aws": {
			Scope{Provider: "aws", Services: []string{"vpc", "s3"}, Regions: []string{"eu-west-1", "us-east-1"}, Profile: "prod", Accounts: []string{"111111111111", "222222222222"}},
			[]string{"discover", "aws", "--services=vpc,s3", "--regions=eu-west-1,us-east-1", "--profile=prod", "--accounts=111111111111,222222222222"},
		},
		// A stack's ARN has no comma, but --cfn-stack takes one stack each
		// time: each is a flag of its own, and the services may be left
		// to the stacks.
		"aws stacks": {
			Scope{Provider: "aws", CloudFormationStacks: []string{"app", "arn:aws:cloudformation:eu-west-2:111111111111:stack/old/1"}, CloudFormationIgnore: []string{"app/Seed"}},
			[]string{"discover", "aws", "--cfn-stack=app", "--cfn-stack=arn:aws:cloudformation:eu-west-2:111111111111:stack/old/1", "--cfn-ignore=app/Seed"},
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
		"azure through azapi": {
			Scope{Provider: "azure", Services: []string{"resource_graph"}, Azapi: true},
			[]string{"discover", "azure", "--services=resource_graph", "--azapi"},
		},
	} {
		if !builtIn(tc.scope.Provider) {
			continue
		}
		args, err := compiled.commandArgs("discover", tc.scope, tc.scope)
		if err != nil || !slices.Equal(args, tc.want) {
			t.Errorf("%s: commandArgs: got %v, %v, want %v", name, args, err, tc.want)
		}
	}
}

// A field for other providers is rejected before anything runs, by
// import and discover alike: aws has an --organization flag too, which
// would list the whole organisation that the user isn't shown.
func TestFieldsForOtherProvidersAreRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
		// needs are the providers the case and its error name.
		needs []string
	}{
		"aws with an organization": {map[string]any{"provider": "aws", "services": []string{"vpc"}, "organization": "true"}, "aws doesn't take organization, which is for google only", []string{"aws", "google"}},
		"azure with regions":       {map[string]any{"provider": "azure", "services": []string{"resource_graph"}, "regions": []string{"westeurope"}}, "azure doesn't take regions, which is for aws and google only", []string{"aws", "azure", "google"}},
		"google with accounts":     {map[string]any{"provider": "google", "services": []string{"iam"}, "projects": []string{"one"}, "accounts": []string{"111"}}, "google doesn't take accounts, which is for aws only", []string{"aws", "google"}},
		"aws with azapi":           {map[string]any{"provider": "aws", "services": []string{"vpc"}, "azapi": true}, "aws doesn't take azapi, which is for azure only", []string{"aws", "azure"}},
	} {
		for _, tool := range []string{"discover", "import"} {
			t.Run(name+" "+tool, func(t *testing.T) {
				needProviders(t, tc.needs...)
				dir := t.TempDir()
				path := filepath.Join(dir, "selection.yaml")
				if err := writeSelection(path); err != nil {
					t.Fatal(err)
				}
				runner := &fakeRunner{}
				session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					return &mcp.ElicitResult{Action: "accept"}, nil
				})
				args := maps.Clone(tc.args)
				args["selection"] = path
				if tool == "import" {
					args["out"] = filepath.Join(dir, "generated")
				}

				res, text := call(t, session, tool, args)

				if !res.IsError || !strings.Contains(text, tc.want) {
					t.Errorf("want an error result with %q, got %s", tc.want, text)
				}
				if len(runner.runs) != 0 {
					t.Errorf("ran: %v", runner.runs)
				}
			})
		}
	}
}

// A run the command rejects, such as a Google scope without a project,
// returns the command's error.
func TestDiscoverReturnsTheCommandsError(t *testing.T) {
	needProviders(t, "google")
	runner := &fakeRunner{discoverExit: report.ExitCouldNotRun, discoverStderr: "google needs --projects, --organization or --folders\n"}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)

	res, text := call(t, session, "discover", map[string]any{"provider": "google", "services": []string{"iam"}, "selection": filepath.Join(dir, "selection.yaml")})

	if !res.IsError || !strings.Contains(text, "google needs --projects") {
		t.Errorf("want an error result with the command's error, got %s", text)
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
		{Scope{Provider: "azure", Services: []string{"resource_graph"}, Azapi: true}, "the azure subscription of ARM_SUBSCRIPTION_ID, what azurerm has no type for through azapi (resource_graph)"},
		{Scope{Provider: "azure", Services: []string{"resource_graph"}, Subscriptions: []string{"sub-1"}, ManagementGroup: "mg", ResourceGroup: "rg"}, "resource group rg of the azure subscriptions sub-1 and every enabled subscription under management group mg (resource_graph)"},
		{Scope{Provider: "aws", CloudFormationStacks: []string{"app"}}, "the aws account, only the resources of CloudFormation stacks app (the services of the stacks' resources)"},
	} {
		if !builtIn(tc.scope.Provider) {
			continue
		}
		if got, err := scopeText(tc.scope, providers.New(tc.scope.Provider).Profile()); err != nil || got != tc.want {
			t.Errorf("scopeText(%+v): got %q, %v, want %q", tc.scope, got, err, tc.want)
		}
	}
	if _, err := scopeText(Scope{Provider: "oracle", Services: []string{"vcn"}}, harvest.Profile{Command: "oracle"}); err == nil {
		t.Error("a provider whose profile can't describe its scope: want an error, not an aws account")
	}
}

// What the user confirms of a provider's scope is its profile's, so a
// fourth provider's takes no change here; the fields of Scope it takes
// are the tools' schema, which names each provider's.
func TestScopeTextOfAnotherProvider(t *testing.T) {
	oracle := harvest.Profile{Command: "oracle", Scope: harvest.Scope{Describe: func(flags map[string][]string) string {
		return "the oracle tenancy in " + strings.Join(flags["regions"], ", ")
	}}}
	got, err := scopeText(Scope{Provider: "oracle", Services: []string{"vcn"}, Regions: []string{"uk-london-1"}}, oracle)
	if want := "the oracle tenancy in uk-london-1 (vcn)"; err != nil || got != want {
		t.Errorf("got %q, %v, want %q", got, err, want)
	}
}

// What the user confirms names every field the provider takes, as its
// profile declares them: a field a provider takes that its case
// didn't say would import more than the user was shown.
func TestScopeTextSaysEveryField(t *testing.T) {
	scope := reflect.TypeFor[Scope]()
	for _, provider := range providers.Names() {
		for i := range scope.NumField() {
			field := scope.Field(i)
			if !slices.Contains(compiled.scopeTakers(field), provider) {
				continue
			}
			s := Scope{Provider: provider, Services: []string{"vpc"}}
			if field.Name == "CloudFormationIgnore" {
				// It is taken with stacks alone.
				s.CloudFormationStacks = []string{"app"}
			}
			profile := providers.New(provider).Profile()
			without, err := scopeText(s, profile)
			if err != nil {
				t.Fatalf("%s: %v", provider, err)
			}
			value := reflect.ValueOf(&s).Elem().Field(i)
			switch value.Kind() {
			case reflect.Bool:
				value.SetBool(true)
			case reflect.String:
				value.SetString("x")
			case reflect.Slice:
				value.Set(reflect.ValueOf([]string{"x"}))
			}
			if with, err := scopeText(s, profile); err != nil || with == without {
				t.Errorf("%s with %s: got %q, %v, the same as without it", provider, jsonName(field), with, err)
			}
		}
	}
}

// Through the tools: a Google scope reaches the google command with its
// projects.
func TestDiscoverScopes(t *testing.T) {
	needProviders(t, "google")
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	runner := &fakeRunner{}
	session := connect(t, runner, dir, nil)
	res, text := call(t, session, "discover", map[string]any{"provider": "google", "services": []string{"iam"}, "projects": []string{"my-project"}, "selection": path})
	if res.IsError {
		t.Errorf("result: %s", text)
	}
	want := []string{"discover", "google", "--services=iam", "--projects=my-project", "--pick=false", "--out=" + filepath.Join(dir, "infraharvest-out"), "--selection=" + path}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
}

// A confirmation only counts for the import the user was asked about.
func TestImportChecksWhatWasConfirmed(t *testing.T) {
	needProviders(t, "aws")
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	tools := newTools(runner, dir)
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
	needProviders(t, "aws")
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
	needProviders(t, "aws")
	root := filepath.Join(t.TempDir(), "work")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRunner{}
	tools := newTools(runner, root)
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
// writes the selection file, the same import again isn't the same, drift
// runs the roots' providers, and drift_accept writes into the code what
// drift only reports.
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
		case "report":
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

// The tools describe the providers the server is passed, which are those
// of the binary it runs, and no others.
func TestToolsDescribeTheProvidersPassed(t *testing.T) {
	needProviders(t, "aws")
	aws, err := compiled.profile("aws")
	if err != nil {
		t.Fatal(err)
	}
	passed := Providers{Profiles: []harvest.Profile{aws}, Check: compiled.Check}
	runner := &fakeRunner{}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := New("test", t.TempDir(), runner.run, runner.check, passed).Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "discover" && tool.Name != "import" {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(schema), `the provider command: aws"`) {
			t.Errorf("%s: the provider's description in %s doesn't name aws alone", tool.Name, schema)
		}
		// The parameters of the providers not passed are left out, and
		// those aws shares name it alone.
		var parsed struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(schema, &parsed); err != nil {
			t.Fatal(err)
		}
		for _, other := range []string{"projects", "organization", "folders", "subscriptions", "management_group", "resource_group", "azapi"} {
			if _, ok := parsed.Properties[other]; ok {
				t.Errorf("%s: the schema has %s, which only providers not passed take", tool.Name, other)
			}
		}
		if got := parsed.Properties["regions"].Description; !strings.HasPrefix(got, "aws only: ") {
			t.Errorf("%s: regions' description %q, want it to name aws alone", tool.Name, got)
		}
		if want := "Where to list depends on the provider: aws takes "; !strings.Contains(tool.Description, want) || strings.Contains(tool.Description, "azure") || strings.Contains(tool.Description, "google") {
			t.Errorf("%s: description %q, want it to say %q of aws alone", tool.Name, tool.Description, want)
		}
	}
}

// A symbolic link a cloned repository plants under the output directory
// doesn't take the report tool, or an import, out of the root.
func TestLinksUnderTheOutputStayUnderRoot(t *testing.T) {
	needProviders(t, "aws")
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
	tools := newTools(runner, root)
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
	needProviders(t, "aws")
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "generated")
	scope := Scope{Provider: "aws", Services: []string{"vpc"}}
	args, err := compiled.commandArgs("import", scope, scope)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, "--selection="+path, "--out="+out, "--format=json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := confirmationState(args, content)
	runner := &fakeRunner{}
	tools := newTools(runner, dir)
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

// The server forgets the runs it asked about longest ago once more than
// maxIssued wait for an answer, so that requests never answered don't
// pile up; a run asked about again counts from then.
func TestIssueForgetsTheOldestUnanswered(t *testing.T) {
	tools := newTools(&fakeRunner{}, t.TempDir())
	for i := range maxIssued {
		tools.issue(fmt.Sprint(i))
	}
	// Asked again, 0 is no longer the oldest: 1 is, which the next run
	// asked about pushes out.
	tools.issue("0")
	tools.issue(fmt.Sprint(maxIssued))
	if len(tools.issued) != maxIssued {
		t.Errorf("waiting on %d runs, want %d", len(tools.issued), maxIssued)
	}
	if tools.redeem("1") {
		t.Error("the run asked about longest ago was redeemed, want it forgotten")
	}
	for _, state := range []string{"0", "2", fmt.Sprint(maxIssued)} {
		if !tools.redeem(state) {
			t.Errorf("run %s wasn't redeemed", state)
		}
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
		if _, err := compiled.commandArgs("import", scope, scope); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	dir := t.TempDir()
	tools := newTools(&fakeRunner{}, dir)
	if _, err := tools.within("out", filepath.Join(dir, "gen\nInto nothing")); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Errorf("a path with a newline: got %v", err)
	}
}

// The confirmation quotes the paths the agent chose.
func TestConfirmationQuotesPaths(t *testing.T) {
	needProviders(t, "aws")
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
	needProviders(t, "aws")
	root := t.TempDir()
	t.Chdir(t.TempDir())
	runner := &fakeRunner{}
	tools := newTools(runner, root)
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
	tools := newTools(&fakeRunner{}, root)
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

// A report that is a hard link to a file elsewhere, which the report
// tool would otherwise read as the report, is refused, as any file the
// agent names is.
func TestReportRefusesAHardLinkedReport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "generated", report.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(secret, []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(dir, "report.md")); err != nil {
		t.Skipf("can't make a hard link: %v", err)
	}
	tools := newTools(&fakeRunner{}, root)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	if res, _, err := tools.report(t.Context(), req, ReportInput{Out: filepath.Join(root, "generated")}); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Errorf("got %+v, %v, want the hard link refused", res, err)
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
	tools := newTools(&fakeRunner{}, root)
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
	needProviders(t, "aws")
	for _, code := range []int{report.ExitInterrupted, report.ExitTerminated, -1} {
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
	needProviders(t, "aws")
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

// The warnings about the run, and those about a directory's configuration
// after its path, are in the result, and its text points to them.
func TestImportReportsWarnings(t *testing.T) {
	needProviders(t, "aws")
	r := report.Report{Coverage: report.Coverage{
		Totals:      report.CoverageTotal{Discovered: 1, Imported: 1},
		Warnings:    []string{"aws can't tell which environment account 123456789012 is"},
		Directories: []report.Directory{{Path: "aws/123456789012/eu-west-2/vpc", Warnings: []string{"a tag is named after an access key ID"}}},
	}}
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
	if !strings.Contains(text, "2 warnings to look at") {
		t.Errorf("text %q doesn't point to the warnings", text)
	}
	content, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got ImportResult
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{r.Warnings[0], "aws/123456789012/eu-west-2/vpc: a tag is named after an access key ID"}
	if !slices.Equal(got.Warnings, want) {
		t.Errorf("warnings: %q, want %q", got.Warnings, want)
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
	tools := newTools(&fakeRunner{}, root)
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

// Each flag a provider's profile declares is a parameter of the tools: a
// flag without one would be one the agent can't set.
func TestScopeHasEveryDeclaredFlag(t *testing.T) {
	scope := reflect.TypeFor[Scope]()
	for _, profile := range compiled.Profiles {
		for _, flag := range profile.Scope.Flags {
			found := false
			for i := range scope.NumField() {
				field := scope.Field(i)
				found = found || flagName(field) == flag && compiled.scopeTakers(field) != nil
			}
			if !found {
				t.Errorf("%s declares --%s, which no field of Scope with a scope tag sets", profile.Command, flag)
			}
		}
	}
}

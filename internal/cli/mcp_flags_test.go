// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// parsingRunner stands for the binary the MCP tools run: it parses their
// arguments with the real command tree, records whether they parsed, and
// prints an empty report, so that a tool goes on to its next run.
type parsingRunner struct {
	mu sync.Mutex
	// errs has an entry per run: nil when its arguments parsed.
	errs []error
}

func (p *parsingRunner) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	cmd, rest, err := NewCmdRoot().Find(args)
	if err == nil && cmd.HasSubCommands() {
		err = fmt.Errorf("%v names no command that runs", args)
	}
	if err == nil {
		err = cmd.ParseFlags(rest)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errs = append(p.errs, err)
	if err != nil {
		return nil, []byte(err.Error()), 2, nil
	}
	return []byte("{}"), nil, 0, nil
}

// take returns the runs' errors since it was last called.
func (p *parsingRunner) take() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	errs := p.errs
	p.errs = nil
	return errs
}

// Every flag an MCP tool can pass is one the command it runs has: each
// parameter of each tool, set on its own, gives arguments that the real
// command tree parses, for at least one provider where the tool takes
// one, and for every provider its providers tag names, while the tool
// refuses it for the others without running anything. The tools build their arguments from their inputs' flag tags, so a
// flag renamed on the command line, or a tag that names none, fails here
// rather than in an agent's run.
func TestMCPToolFlagsExist(t *testing.T) {
	for _, p := range referenceProviders {
		if providers.New(p) == nil {
			t.Skipf("built without %s", p)
		}
	}
	dir := t.TempDir()
	selectionFile := filepath.Join(dir, "selection.yaml")
	if err := (&selection.File{Version: selection.Version}).Save(selectionFile); err != nil {
		t.Fatal(err)
	}
	decisions := filepath.Join(dir, "drift.yaml")
	if err := os.WriteFile(decisions, fmt.Appendf(nil, "version: %d\n", importer.DriftDecisionsVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "catalogue"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &parsingRunner{}
	// The runs parse the arguments; what the command's checks make of
	// these values, such as services named x, is beside the point here.
	session := connectMCP(t, dir, runner.run, func(context.Context, []string) error { return nil }, nil)

	// The parameters each tool needs to run at all, and the values of the
	// others that aren't any text, list or number.
	base := map[string]map[string]any{
		"discover":     {"selection": selectionFile},
		"import":       {"selection": selectionFile, "out": "generated"},
		"drift":        {"out": "generated"},
		"drift_accept": {"out": "generated", "keep": []string{"all"}},
	}
	values := map[string]any{
		"engine":         importer.EngineTofu,
		"engine_timeout": "30m",
		"catalogue":      "catalogue",
		"decisions":      decisions,
		"accept":         []string{"all"},
		"keep":           []string{"all"},
	}

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		required, ok := base[tool.Name]
		if !ok {
			// report runs nothing.
			continue
		}
		properties, err := schemaProperties(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		// The provider command each call runs: none for drift.
		names := []string{""}
		if _, ok := properties["provider"]; ok {
			names = providers.Names()
		}
		for property, kind := range properties {
			if property == "provider" || property == "selection" || property == "out" {
				continue
			}
			value, ok := values[property]
			if !ok {
				value = sampleValue(kind)
			}
			takers := scopeProviders(property)
			var parsed []string
			var failures []error
			for _, name := range names {
				args := map[string]any{property: value}
				for k, v := range required {
					if k != property {
						args[k] = v
					}
				}
				if name != "" {
					args["provider"] = name
				}
				res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				errs := runner.take()
				if takers != nil && !slices.Contains(takers, name) {
					if len(errs) != 0 || !res.IsError {
						t.Errorf("%s with %s on %q: ran %v, want it refused", tool.Name, property, name, errs)
					}
					continue
				}
				if len(errs) == 0 {
					t.Errorf("%s with %s on %q ran nothing: %+v", tool.Name, property, name, res.Content)
					continue
				}
				if err := firstError(errs); err != nil {
					failures = append(failures, err)
					continue
				}
				parsed = append(parsed, name)
			}
			if len(parsed) == 0 {
				t.Errorf("%s: the command rejects the flag of %s for every provider: %v", tool.Name, property, failures)
			}
			if takers != nil && !slices.Equal(parsed, takers) {
				t.Errorf("%s: the command takes the flag of %s for %v, not every provider its tag names, %v: %v", tool.Name, property, parsed, takers, failures)
			}
		}
	}
}

// scopeProviders returns the providers that the field of mcpserver.Scope
// an agent sends as name is for, as its providers tag names them, or none
// for a field every provider takes.
func scopeProviders(name string) []string {
	scope := reflect.TypeFor[mcpserver.Scope]()
	for i := range scope.NumField() {
		field := scope.Field(i)
		if json, _, _ := strings.Cut(field.Tag.Get("json"), ","); json != name {
			continue
		}
		if tag, ok := field.Tag.Lookup("providers"); ok {
			return strings.Split(tag, ",")
		}
	}
	return nil
}

// connectMCP returns a client session with an MCP server whose tools run
// run and check with check, and whose user confirms everything, after
// asked, if set, is called.
func connectMCP(t *testing.T, root string, run mcpserver.Runner, check mcpserver.Checker, asked func()) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New("test", root, run, check).Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	confirm := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		if asked != nil {
			asked()
		}
		return &mcp.ElicitResult{Action: "accept"}, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, &mcp.ClientOptions{ElicitationHandler: confirm})
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// schemaProperties returns the JSON type of each property of a tool's
// input schema.
func schemaProperties(schema any) (map[string]string, error) {
	content, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var s struct {
		// Type is a type, or a list of them, such as ["null", "array"].
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(content, &s); err != nil {
		return nil, err
	}
	kinds := make(map[string]string, len(s.Properties))
	for name, p := range s.Properties {
		var types []string
		if err := json.Unmarshal(p.Type, &types); err != nil {
			var kind string
			if err := json.Unmarshal(p.Type, &kind); err != nil {
				return nil, fmt.Errorf("%s: type %s", name, p.Type)
			}
			types = []string{kind}
		}
		types = slices.DeleteFunc(types, func(t string) bool { return t == "null" })
		if len(types) > 0 {
			kinds[name] = types[0]
		}
	}
	return kinds, nil
}

// sampleValue returns a value of the JSON type kind.
func sampleValue(kind string) any {
	switch kind {
	case "array":
		return []string{"x"}
	case "boolean":
		return true
	case "integer", "number":
		return 2
	}
	return "x"
}

// firstError returns the first of errs that isn't nil.
func firstError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// --check checks a provider command's flags, and the settings of the
// configuration file, as the run would, and exits without listing
// anything: a run that would list exits 0 with nothing written, and one
// the run would refuse exits 2 with the run's error.
func TestCheckFlag(t *testing.T) {
	for _, p := range referenceProviders {
		if providers.New(p) == nil {
			t.Skipf("built without %s", p)
		}
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	// What a configuration file sets is checked as the flags are.
	config := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("version: 1\n"+content), 0o644); err != nil {
			t.Fatal(err)
		}
		return "--config=" + path
	}
	services := config("services.yaml", "providers:\n  aws:\n    services: [vpc]\n")
	unknownService := config("unknown-service.yaml", "providers:\n  aws:\n    services: [nothing]\n")
	check := config("check.yaml", "settings:\n  check: true\n")
	projects := config("projects.yaml", "settings:\n  layout: \"{out}/{region}\"\nproviders:\n  google:\n    projects: [one, two]\n")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import", "aws", "--services=vpc", "--all", "--out=" + out}, ""},
		{[]string{"import", "aws", "--all", services, "--out=" + out}, ""},
		{[]string{"import", "aws", "--all", unknownService, "--out=" + out}, "aws has no service nothing"},
		{[]string{"import", "aws", "--services=vpc", "--all", check, "--out=" + out}, `"check" is for the command line`},
		{[]string{"import", "google", "--services=iam", "--all", projects, "--out=" + out}, "needs {account} to keep several projects apart"},
		{[]string{"discover", "google", "--services=iam", "--projects=p", "--out=" + out}, ""},
		{[]string{"import", "aws", "--all", "--out=" + out}, "--services is required"},
		// Before the other checks, as the command line always said it.
		{[]string{"import", "aws", "--engine=pulumi", "--out=" + out}, "--services is required"},
		{[]string{"import", "aws", "--services=vpc", "--out=" + out}, "selection"},
		{[]string{"import", "google", "--services=iam", "--all", "--out=" + out}, "say which projects"},
		{[]string{"import", "google", "--services=iam", "--all", "--projects=one,two", "--layout={out}/{region}", "--out=" + out}, "needs {account} to keep several projects apart"},
		{[]string{"import", "google", "--services=iam", "--all", "--projects=one", "--layout={out}/{region}", "--out=" + out}, ""},
		{[]string{"import", "azure", "--services=resource_graph", "--all", "--subscriptions=one,two", "--layout={out}/{region}", "--out=" + out}, "needs {account} to keep several subscriptions apart"},
		{[]string{"import", "azure", "--services=resource_graph", "--all", "--regions=westeurope", "--out=" + out}, "unknown flag: --regions"},
		{[]string{"discover", "aws", "--services=vpc", "--accounts=111122223333", "--organization", "--out=" + out}, "either --accounts or --organization"},
		{[]string{"import", "aws", "--services=vpc", "--all", "--engine=pulumi", "--out=" + out}, `unknown --engine "pulumi"`},
		// After a --, every flag is an argument, --check too were it
		// last: the command takes none, so nothing is imported.
		{[]string{"import", "aws", "--services=vpc", "--all", "--out=" + out, "--"}, ""},
		{[]string{"import", "aws", "--services=vpc", "--all", "--", "--out=" + out}, "unknown command"},
	} {
		err := checkRun(t.Context(), tc.args)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%v: %v", tc.args, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%v: got %v, want %q", tc.args, err, tc.want)
		case tc.want != "" && importer.ExitCode(err) != report.ExitCouldNotRun:
			// As the usage guide says, for a pipeline to tell.
			t.Errorf("%v: exit code %d, want %d", tc.args, importer.ExitCode(err), report.ExitCouldNotRun)
		}
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--check wrote %s: %v", out, err)
	}
}

// The MCP import tool checks an import with the command's own checks
// before the user is asked to confirm it, and returns the command's error.
func TestMCPImportChecksBeforeAsking(t *testing.T) {
	for _, p := range referenceProviders {
		if providers.New(p) == nil {
			t.Skipf("built without %s", p)
		}
	}
	dir := t.TempDir()
	selectionFile := filepath.Join(dir, "selection.yaml")
	if err := (&selection.File{Version: selection.Version}).Save(selectionFile); err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, []string) ([]byte, []byte, int, error) {
		t.Error("an import the checks refuse ran")
		return nil, nil, 2, nil
	}
	asked := false
	session := connectMCP(t, dir, run, checkRun, func() { asked = true })
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"provider": "azure", "services": []string{"resource_graph"}, "regions": []string{"westeurope"}}, "azure doesn't take regions"},
		{map[string]any{"provider": "google", "services": []string{"iam"}}, "say which projects"},
		{map[string]any{"provider": "aws"}, "--services is required"},
		{map[string]any{"provider": "aws", "services": []string{"vpc"}, "incremental": true, "regenerate": true}, "either --regenerate or --incremental"},
	} {
		tc.args["selection"], tc.args["out"] = selectionFile, "generated"
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "import", Arguments: tc.args})
		if err != nil {
			t.Fatal(err)
		}
		text, _ := json.Marshal(res.Content)
		if !res.IsError || !strings.Contains(string(text), tc.want) {
			t.Errorf("%v: got %s, want an error with %q", tc.args, text, tc.want)
		}
	}
	if asked {
		t.Error("the user was asked to confirm an import the checks refuse")
	}
}

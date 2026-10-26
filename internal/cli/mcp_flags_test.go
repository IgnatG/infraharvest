// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
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
// one. The tools build their arguments from their inputs' flag tags, so a
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
	session := connectMCP(t, dir, runner.run)

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
		}
	}
}

// connectMCP returns a client session with an MCP server whose tools run
// run, and whose user confirms everything.
func connectMCP(t *testing.T, root string, run mcpserver.Runner) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New("test", root, run).Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	confirm := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
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

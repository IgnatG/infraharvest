// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// readConfig records the content of the configuration file args pass.
func (f *fakeRunner) readConfig(args []string) {
	for _, a := range args {
		if path, ok := strings.CutPrefix(a, "--config="); ok {
			content, err := os.ReadFile(path)
			if err != nil {
				content = []byte(err.Error())
			}
			f.configs = append(f.configs, string(content))
		}
	}
}

// sampleConfig sets what the tools take: settings, a provider's, a state
// backend and environments.
const sampleConfig = `version: 1
settings:
  engine: tofu
providers:
  aws:
    regions: [eu-west-2]
backend:
  s3: { bucket: states, region: eu-west-2 }
environments:
  prod: { aws: { accounts: ["111111111111"] } }
`

// configFixture makes, under a root, a configuration file of content, a
// selection file and an output directory, and returns the root and the
// arguments of each tool with the configuration file.
func configFixture(t *testing.T, content string) (string, map[string]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "infraharvest.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "selection.yaml")
	if err := writeSelection(path); err != nil {
		t.Fatal(err)
	}
	out := generatedOut(t, dir)
	importInput := importArgs(path, out)
	importInput["config"] = "infraharvest.yaml"
	return dir, map[string]map[string]any{
		"discover":     {"provider": "aws", "services": []string{"vpc"}, "selection": path, "config": "infraharvest.yaml"},
		"import":       importInput,
		"drift":        {"out": out, "config": "infraharvest.yaml"},
		"drift_accept": {"out": out, "config": "infraharvest.yaml", "rules": []string{"accept all"}},
	}
}

// A configuration file under the root, relative to it, is read once and
// checked; the check before the user is asked and the run read a private
// copy of what was checked, and the user is told what it sets.
func TestConfig(t *testing.T) {
	dir, tools := configFixture(t, sampleConfig)
	config := filepath.Join(dir, "infraharvest.yaml")
	for tool, want := range map[string]struct {
		// reads counts the runs and checks that read it.
		reads int
		asked bool
	}{
		"discover":     {1, false},
		"import":       {3, true},
		"drift":        {1, false},
		"drift_accept": {2, true},
	} {
		t.Run(tool, func(t *testing.T) {
			runner := &fakeRunner{}
			var asked string
			session := connect(t, runner, dir, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = req.Params.Message
				return &mcp.ElicitResult{Action: "accept"}, nil
			})

			if res, text := call(t, session, tool, tools[tool]); res.IsError {
				t.Fatalf("result: %s", text)
			}
			if len(runner.configs) != want.reads {
				t.Fatalf("read the configuration file %d times, want %d: %q", len(runner.configs), want.reads, runner.configs)
			}
			for _, content := range runner.configs {
				if content != sampleConfig {
					t.Errorf("read %q, want the checked content", content)
				}
			}
			for _, args := range append(runner.runs, runner.checked...) {
				for _, a := range args {
					if a == "--config="+config {
						t.Errorf("%v reads the file itself, not a private copy", args)
					}
				}
			}
			if !want.asked {
				return
			}
			for _, s := range []string{`The configuration file "` + config + `" applies`, `engine="tofu"`, `aws settings regions="[eu-west-2]"`, `s3 bucket "states"`, "environments prod"} {
				if !strings.Contains(asked, s) {
					t.Errorf("confirmation %q misses %q", asked, s)
				}
			}
		})
	}
}

// A configuration file is refused before anything runs when it is outside
// the root, or sets what the tools don't take: a binary to run, such as
// terraform-path, which the read-only drift would run too, a directory,
// such as a catalogue outside the root, or the organisation's modules.
func TestConfigRefused(t *testing.T) {
	outside := t.TempDir()
	for name, tc := range map[string]struct {
		content string
		want    string
	}{
		"terraform-path":           {"version: 1\nsettings:\n  terraform-path: " + filepath.Join(outside, "terraform") + "\n", "settings: terraform-path isn't a setting the tools take"},
		"a provider's binary":      {"version: 1\nproviders:\n  aws:\n    terraform-path: /bin/sh\n", "providers: aws: terraform-path isn't a setting the tools take"},
		"a catalogue outside":      {"version: 1\nsettings:\n  catalogue: " + outside + "\n", "settings: catalogue isn't a setting the tools take"},
		"the organisation modules": {"version: 1\nmodules:\n  - source: " + outside + "\n", "modules"},
		"outside the root":         {"", "must be under"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, tools := configFixture(t, tc.content)
			if tc.content == "" {
				path := filepath.Join(outside, "infraharvest.yaml")
				if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
					t.Fatal(err)
				}
				for _, args := range tools {
					args["config"] = path
				}
			}
			for tool, args := range tools {
				runner := &fakeRunner{}
				session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					return &mcp.ElicitResult{Action: "accept"}, nil
				})
				if res, text := call(t, session, tool, maps.Clone(args)); !res.IsError || !strings.Contains(text, tc.want) {
					t.Errorf("%s: got %s, want it refused with %q", tool, text, tc.want)
				}
				if len(runner.runs) != 0 || len(runner.checked) != 0 {
					t.Errorf("%s: ran %v, checked %v", tool, runner.runs, runner.checked)
				}
			}
		})
	}
}

// The confirmation is for the configuration file as it was when the user
// was asked: one the agent edits before the run isn't used, and nothing
// runs with it.
func TestConfigEditedAfterConfirmation(t *testing.T) {
	for _, tool := range []string{"import", "drift_accept"} {
		t.Run(tool, func(t *testing.T) {
			dir, tools := configFixture(t, sampleConfig)
			edited := strings.Replace(sampleConfig, "tofu", "terraform", 1)
			runner := &fakeRunner{}
			session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return &mcp.ElicitResult{Action: "accept"}, os.WriteFile(filepath.Join(dir, "infraharvest.yaml"), []byte(edited), 0o644)
			})

			if res, text := call(t, session, tool, tools[tool]); !res.IsError || !strings.Contains(text, "didn't confirm") {
				t.Errorf("got %s, want it refused", text)
			}
			// Only the check before asking ran, with the file the user confirmed.
			if want := map[string]int{"import": 0, "drift_accept": 1}[tool]; len(runner.runs) != want {
				t.Errorf("runs %v, want %d", runner.runs, want)
			}
			if tool == "drift_accept" && (len(runner.configs) != 1 || runner.configs[0] != sampleConfig) {
				t.Errorf("the runs read %q, want the confirmed content", runner.configs)
			}
		})
	}
}

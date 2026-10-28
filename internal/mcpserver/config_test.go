// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/config"
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

// sampleConfig sets what discover and import take: settings, a
// provider's, a state backend and environments; and a section of another
// provider's, which an aws run doesn't apply.
const sampleConfig = `version: 1
settings:
  engine: tofu
providers:
  aws:
    regions: [eu-west-2]
  google:
    projects: [p1]
backend:
  s3: { bucket: states, region: eu-west-2 }
environments:
  prod: { aws: { accounts: ["111111111111"] } }
`

// sampleDriftConfig sets what drift and drift_accept take.
const sampleDriftConfig = `version: 1
settings:
  engine: tofu
  engine-timeout: 30m
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

// fixtureNeeds are the providers the arguments configFixture returns for
// each tool take: discover and import read an aws account.
var fixtureNeeds = map[string][]string{"discover": {"aws"}, "import": {"aws"}}

// A configuration file under the root, relative to it, is read once and
// checked; the check before the user is asked and the run read a private
// copy of what was checked, and the user is told all that it sets that
// applies to the run, and nothing that doesn't, such as another
// provider's section.
func TestConfig(t *testing.T) {
	for tool, want := range map[string]struct {
		content string
		// reads counts the runs and checks that read it.
		reads int
		// asked is what the confirmation says it sets, "" for a tool that
		// doesn't ask.
		asked string
		// needs are the providers the tool's arguments and the file's
		// sections take.
		needs []string
	}{
		"discover":     {sampleConfig, 1, "", []string{"aws", "google"}},
		"import":       {sampleConfig, 3, `settings {"engine": "tofu"}; providers {"aws": {"regions": ["eu-west-2"]}}; backend {s3: {bucket: "states", region: "eu-west-2"}}; environments {"prod": {aws: {accounts: ["111111111111"]}}}.`, []string{"aws", "google"}},
		"drift":        {sampleDriftConfig, 1, `settings {"engine": "tofu", "engine-timeout": "30m"}.`, nil},
		"drift_accept": {sampleDriftConfig, 1, `settings {"engine": "tofu", "engine-timeout": "30m"}.`, nil},
	} {
		t.Run(tool, func(t *testing.T) {
			needProviders(t, want.needs...)
			dir, tools := configFixture(t, want.content)
			config := filepath.Join(dir, "infraharvest.yaml")
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
				if content != want.content {
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
			if want.asked == "" {
				return
			}
			if s := fmt.Sprintf("The configuration file %q applies, setting ", config) + want.asked; !strings.Contains(asked, s) {
				t.Errorf("confirmation %q misses %q", asked, s)
			}
		})
	}
}

// A configuration file is refused before anything runs when it is outside
// the root, as written or where a link under the root leads, or sets what
// the tools don't take: a binary to run, such as terraform-path, which the
// read-only drift would run too, in any provider's section, a directory,
// such as a catalogue outside the root or the policies conftest runs, or
// the organisation's modules; or a mode, such as regenerate, which only
// the tool's inputs set, as the confirmation describes the run from them.
func TestConfigRefused(t *testing.T) {
	outside := t.TempDir()
	outsideConfig := filepath.Join(outside, "infraharvest.yaml")
	if err := os.WriteFile(outsideConfig, []byte(sampleDriftConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		content string
		// place puts the file under the root dir in another way than
		// writing content there, and returns the path to pass.
		place func(t *testing.T, dir string) string
		want  string
	}{
		"terraform-path":            {content: "version: 1\nsettings:\n  terraform-path: " + filepath.Join(outside, "terraform") + "\n", want: "settings: terraform-path isn't a setting the"},
		"a provider's binary":       {content: "version: 1\nproviders:\n  aws:\n    terraform-path: /bin/sh\n", want: "providers: aws: terraform-path isn't a setting the"},
		"another provider's binary": {content: "version: 1\nproviders:\n  google:\n    terraform-path: /bin/sh\n", want: "providers: google: terraform-path isn't a setting the"},
		"a catalogue outside":       {content: "version: 1\nsettings:\n  catalogue: " + outside + "\n", want: "settings: catalogue isn't a setting the"},
		"a policy directory":        {content: "version: 1\nsettings:\n  policy: [" + outside + "]\n", want: "settings: policy isn't a setting the"},
		"a provider's policies":     {content: "version: 1\nproviders:\n  aws:\n    policy: " + outside + "\n", want: "providers: aws: policy isn't a setting the"},
		"the organisation modules":  {content: "version: 1\nmodules:\n  - source: " + outside + "\n", want: "modules"},
		"regenerate":                {content: "version: 1\nsettings:\n  regenerate: true\n", want: "settings: regenerate isn't a setting the"},
		"incremental":               {content: "version: 1\nproviders:\n  aws:\n    incremental: true\n", want: "providers: aws: incremental isn't a setting the"},
		"force-unlock":              {content: "version: 1\nsettings:\n  force-unlock: true\n", want: "settings: force-unlock isn't a setting the"},
		"outside the root": {place: func(*testing.T, string) string {
			return outsideConfig
		}, want: "must be under"},
		"a link under the root to outside it": {place: func(t *testing.T, dir string) string {
			link := filepath.Join(dir, "linked.yaml")
			if err := os.Symlink(outsideConfig, link); err != nil {
				t.Skipf("can't make a symbolic link: %v", err)
			}
			return "linked.yaml"
		}, want: "must be under"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, tools := configFixture(t, tc.content)
			if tc.place != nil {
				path := tc.place(t, dir)
				for _, args := range tools {
					args["config"] = path
				}
			}
			for tool, args := range tools {
				if !builtIn(fixtureNeeds[tool]...) {
					continue
				}
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

// Each tool takes the settings its command applies from a configuration
// file, and refuses the rest, so that what it says the file sets is what
// applies: drift applies engine and engine-timeout under settings alone,
// and neither the import's scope nor its backend or environments; discover
// and import apply what the import's parameters set for the run's
// provider, and neither drift's own settings nor another provider's.
func TestConfigSettingsByTool(t *testing.T) {
	importing, drifting := []string{"discover", "import"}, []string{"drift", "drift_accept"}
	all := append(slices.Clone(importing), drifting...)
	for name, tc := range map[string]struct {
		content string
		refused []string
		want    string
		// needs are the providers the file's sections take.
		needs []string
	}{
		"engine":                {"version: 1\nsettings:\n  engine: tofu\n", nil, "", nil},
		"query-types":           {"version: 1\nproviders:\n  aws:\n    query-types: [aws_flow_log]\n", drifting, "providers: aws: query-types isn't a setting the drift", nil},
		"engine-timeout":        {"version: 1\nsettings:\n  engine-timeout: 30m\n", importing, "settings: engine-timeout isn't a setting the", nil},
		"the import's scope":    {"version: 1\nproviders:\n  aws:\n    regions: [eu-west-2]\n", drifting, "providers: aws: regions isn't a setting the drift", nil},
		"a provider's engine":   {"version: 1\nproviders:\n  aws:\n    engine: tofu\n", drifting, "it takes none there", nil},
		"drift's settings list": {"version: 1\nsettings:\n  services: [vpc]\n", drifting, "which are engine, engine-timeout;", nil},
		"the backend":           {"version: 1\nbackend:\n  s3: { bucket: states, region: eu-west-2 }\n", drifting, "backend: the drift", nil},
		"the environments":      {"version: 1\nenvironments:\n  prod: { aws: { accounts: [\"111111111111\"] } }\n", drifting, "environments: the drift", []string{"aws"}},
		"root":                  {"version: 1\nsettings:\n  root: [aws/*]\n", all, "settings: root isn't a setting the", nil},
		"environment":           {"version: 1\nsettings:\n  environment: [prod]\n", all, "settings: environment isn't a setting the", nil},
		"parallel":              {"version: 1\nsettings:\n  parallel: 2\n", all, "settings: parallel isn't a setting the", nil},
		"another provider's":    {"version: 1\nsettings:\n  projects: [p1]\n", all, "settings: projects isn't a setting the", nil},
		"its own section":       {"version: 1\nproviders:\n  google:\n    projects: [p1]\n", drifting, "providers: google: projects isn't a setting the drift", []string{"google"}},
	} {
		t.Run(name, func(t *testing.T) {
			needProviders(t, tc.needs...)
			dir, tools := configFixture(t, tc.content)
			for _, tool := range all {
				if !builtIn(fixtureNeeds[tool]...) {
					continue
				}
				session := connect(t, &fakeRunner{}, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					return &mcp.ElicitResult{Action: "accept"}, nil
				})
				res, text := call(t, session, tool, tools[tool])
				switch {
				case slices.Contains(tc.refused, tool) && (!res.IsError || !strings.Contains(text, tc.want)):
					t.Errorf("%s: got %s, want it refused with %q", tool, text, tc.want)
				case !slices.Contains(tc.refused, tool) && res.IsError:
					t.Errorf("%s: got %s, want it taken", tool, text)
				}
			}
		})
	}
}

// The confirmation of an import says what it reads with the scope the
// configuration file sets where the input doesn't, as the command applies
// it: the input's first, then the file's settings, then the provider's
// section.
func TestConfigScopeInConfirmation(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		// input is what the import's input sets, besides the provider and
		// the files.
		input map[string]any
		want  string
	}{
		"google's projects and regions": {
			"version: 1\nproviders:\n  google:\n    projects: [p1]\n    regions: [europe-west1]\n",
			map[string]any{"provider": "google", "services": []string{"iam"}},
			"infraharvest reads the google projects p1 in europe-west1 (iam)",
		},
		"aws accounts and services": {
			"version: 1\nsettings:\n  services: [vpc, s3]\n  accounts: [\"111111111111\", \"222222222222\"]\n",
			map[string]any{"provider": "aws"},
			"infraharvest reads the aws accounts 111111111111, 222222222222 (vpc, s3)",
		},
		"a list as one text": {
			"version: 1\nproviders:\n  aws:\n    services: vpc,s3\n",
			map[string]any{"provider": "aws"},
			"infraharvest reads the aws account (vpc, s3)",
		},
		"the input wins": {
			"version: 1\nsettings:\n  regions: [eu-west-3]\n",
			map[string]any{"provider": "aws", "services": []string{"vpc"}, "regions": []string{"eu-west-1"}},
			"infraharvest reads the aws account in eu-west-1 (vpc)",
		},
		"the settings win over the provider's": {
			"version: 1\nsettings:\n  regions: [eu-west-3]\nproviders:\n  aws:\n    regions: [eu-west-2]\n",
			map[string]any{"provider": "aws", "services": []string{"vpc"}},
			"infraharvest reads the aws account in eu-west-3 (vpc)",
		},
		"the stacks": {
			"version: 1\nproviders:\n  aws:\n    cfn-stack: [app]\n",
			map[string]any{"provider": "aws"},
			"infraharvest reads the aws account, only the resources of CloudFormation stacks app (the services of the stacks' resources)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			needProviders(t, tc.input["provider"].(string))
			dir, tools := configFixture(t, tc.content)
			input := map[string]any{"selection": tools["import"]["selection"], "out": tools["import"]["out"], "config": "infraharvest.yaml"}
			maps.Copy(input, tc.input)
			var asked string
			session := connect(t, &fakeRunner{}, dir, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = req.Params.Message
				return &mcp.ElicitResult{Action: "decline"}, nil
			})
			call(t, session, "import", input)
			if !strings.Contains(asked, tc.want) {
				t.Errorf("confirmation %q misses %q", asked, tc.want)
			}
		})
	}
}

// A scope value of the configuration file that the confirmation would
// name is checked as the input's: one that isn't named like a region, or
// has a control character that could make the confirmation say something
// else, is refused before the user is asked.
func TestConfigScopeChecked(t *testing.T) {
	for name, content := range map[string]string{
		"a region":     "version: 1\nproviders:\n  aws:\n    regions: [\"eu-west-2 and every account\"]\n",
		"a profile":    "version: 1\nproviders:\n  aws:\n    profile: \"prod\\nimports nothing\"\n",
		"an account":   "version: 1\nsettings:\n  accounts: [\"\"]\n",
		"a query type": "version: 1\nsettings:\n  query-types: [\"aws_flow_log and every type\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			needProviders(t, "aws")
			dir, tools := configFixture(t, content)
			args := maps.Clone(tools["import"])
			delete(args, "regions")
			delete(args, "profile")
			asked := false
			session := connect(t, &fakeRunner{}, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = true
				return &mcp.ElicitResult{Action: "accept"}, nil
			})
			if res, text := call(t, session, "import", args); !res.IsError || asked {
				t.Errorf("got %s, asked %v: want it refused before asking", text, asked)
			}
		})
	}
}

// The confirmation is for the configuration file as it was when the user
// was asked: one the agent edits before the run isn't used, and nothing
// runs with it.
func TestConfigEditedAfterConfirmation(t *testing.T) {
	for _, tool := range []string{"import", "drift", "drift_accept"} {
		t.Run(tool, func(t *testing.T) {
			// sampleConfig has a section of google's too.
			if tool == "import" {
				needProviders(t, "aws", "google")
			}
			content := map[string]string{"import": sampleConfig, "drift": sampleDriftConfig, "drift_accept": sampleDriftConfig}[tool]
			dir, tools := configFixture(t, content)
			edited := strings.Replace(content, "tofu", "terraform", 1)
			runner := &fakeRunner{}
			session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return &mcp.ElicitResult{Action: "accept"}, os.WriteFile(filepath.Join(dir, "infraharvest.yaml"), []byte(edited), 0o644)
			})

			if res, text := call(t, session, tool, tools[tool]); !res.IsError || !strings.Contains(text, "didn't confirm") {
				t.Errorf("got %s, want it refused", text)
			}
			if len(runner.runs) != 0 {
				t.Errorf("runs %v, want none", runner.runs)
			}
		})
	}
}

// The confirmation shows all that a configuration file sets, every value
// quoted: each field of every kind of state backend, and each
// environment's scopes, tags and tokens.
func TestConfigText(t *testing.T) {
	for _, tc := range []struct {
		content, want string
		// need is the provider whose section the file has.
		need string
	}{
		{
			"version: 1\nbackend:\n  s3: { bucket: states, region: eu-west-2, key_prefix: infra, kms_key_id: alias/state }\nenvironments:\n  prod:\n    aws: { accounts: [\"111111111111\"] }\n    tags: { Env: \"prod\\\" now\" }\n    tokens: [prd, live]\n",
			`backend {s3: {bucket: "states", region: "eu-west-2", key_prefix: "infra", kms_key_id: "alias/state"}}; environments {"prod": {aws: {accounts: ["111111111111"]}, tags: {"Env": "prod\" now"}, tokens: ["prd", "live"]}}`, "aws",
		},
		{
			"version: 1\nbackend:\n  azurerm: { resource_group_name: rg, storage_account_name: sa, container_name: c, key_prefix: p }\nenvironments:\n  dev: { azure: { subscriptions: [\"00000000-0000-0000-0000-000000000001\"] } }\n",
			`backend {azurerm: {resource_group_name: "rg", storage_account_name: "sa", container_name: "c", key_prefix: "p"}}; environments {"dev": {azure: {subscriptions: ["00000000-0000-0000-0000-000000000001"]}}}`, "azure",
		},
		{
			"version: 1\nsettings: { services: [iam, gcs], azapi: true }\nbackend:\n  gcs: { bucket: states, prefix: infra }\nenvironments:\n  dev: { google: { projects: [p-dev] } }\n",
			`settings {"azapi": true, "services": ["iam", "gcs"]}; backend {gcs: {bucket: "states", prefix: "infra"}}; environments {"dev": {google: {projects: ["p-dev"]}}}`, "google",
		},
		{"version: 1\n", "nothing", ""},
	} {
		if tc.need != "" && !builtIn(tc.need) {
			continue
		}
		path := filepath.Join(t.TempDir(), "infraharvest.yaml")
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := config.Load(path, providers.Profiles())
		if err != nil {
			t.Fatal(err)
		}
		if got := configText(f, configUse{tool: "import", provider: "aws"}); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}

// A selection, decisions or configuration file under the root that is a
// hard link to a file outside it, which the checks of its path can't
// see, is refused before anything runs or the user is asked.
func TestHardLinkedFilesRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows' file info doesn't count hard links")
	}
	outside := filepath.Join(t.TempDir(), "file.yaml")
	if err := writeSelection(outside); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tool, param string
	}{{"import", "selection"}, {"drift_accept", "decisions"}, {"import", "config"}, {"drift", "config"}} {
		t.Run(tc.tool+" "+tc.param, func(t *testing.T) {
			needProviders(t, fixtureNeeds[tc.tool]...)
			dir, tools := configFixture(t, sampleDriftConfig)
			if err := os.Link(outside, filepath.Join(dir, "linked.yaml")); err != nil {
				t.Skipf("can't make a hard link: %v", err)
			}
			args := maps.Clone(tools[tc.tool])
			delete(args, "config")
			args[tc.param] = "linked.yaml"
			runner := &fakeRunner{}
			asked := false
			session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = true
				return &mcp.ElicitResult{Action: "accept"}, nil
			})
			if res, text := call(t, session, tc.tool, args); !res.IsError || !strings.Contains(text, "hard link") || asked || len(runner.runs) != 0 {
				t.Errorf("got %s, asked %v, ran %v: want it refused", text, asked, runner.runs)
			}
		})
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package action

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// fakeExporter stands in for infraharvest: it logs its arguments and the
// export key, and writes a graph document to --out-file, or fails when
// FAKE_EXPORT_FAIL is set.
const fakeExporter = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_ARGS_LOG"
printf 'key=%s\n' "$INFRAHARVEST_EXPORT_KEY" >> "$FAKE_ARGS_LOG"
if [[ -n "$FAKE_EXPORT_FAIL" ]]; then echo "unknown document graph" >&2; exit 2; fi
for arg in "$@"; do
  case "$arg" in --out-file=*) file=${arg#--out-file=} ;; esac
done
echo '{"schema":"infraharvest/graph/v1"}' > "$file"
`

// fakeNode stands in for node running the Terragram CLI: it logs its
// arguments and prints a flowchart, or what FAKE_NODE_OUT says. With
// FAKE_NODE_FAIL it fails; with FAKE_NODE_FAIL_BASE, when it is given a base;
// with FAKE_NODE_EMPTY it prints nothing.
const fakeNode = `#!/usr/bin/env bash
printf 'node %s\n' "$*" >> "$FAKE_ARGS_LOG"
if [[ -n "$FAKE_NODE_FAIL" ]]; then echo "boom" >&2; exit 1; fi
if [[ -n "$FAKE_NODE_FAIL_BASE" && " $* " == *" --base "* ]]; then echo "old graph" >&2; exit 1; fi
if [[ -n "$FAKE_NODE_EMPTY" ]]; then exit 0; fi
if [[ -n "$FAKE_NODE_OUT" ]]; then printf '%s\n' "$FAKE_NODE_OUT"; else printf 'flowchart TB\n  a["aws_vpc"]\n'; fi
`

// earlier is the graph an earlier run signed, where evidence.sh writes it.
const earlier = "out/_reports/evidence/graph.json"

// diagramResult is what a run of diagram.sh left.
type diagramResult struct {
	output  string // the log
	outputs string // $GITHUB_OUTPUT
	summary string // $GITHUB_STEP_SUMMARY
	args    string // what the fakes logged
	dir     string
	err     error
}

// runDiagram runs diagram.sh in a directory that has the fake infraharvest
// and node, the Terragram CLI file (tmp/terragram.mjs) and the files the
// test asks for. $DIR in a value of env is that directory.
func runDiagram(t *testing.T, mode string, env, files map[string]string) diagramResult {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"tmp/terragram.mjs": "// the CLI"}
	for name, content := range files {
		all[name] = content
	}
	for name, content := range all {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"infraharvest": fakeExporter, "node": fakeNode} {
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := filepath.Abs("diagram.sh")
	if err != nil {
		t.Fatal(err)
	}
	githubOutput := filepath.Join(dir, "github-output")
	summary := filepath.Join(dir, "summary.md")
	argsLog := filepath.Join(dir, "args.log")
	cmd := exec.CommandContext(t.Context(), "bash", script, mode)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"RUNNER_TEMP=" + filepath.Join(dir, "tmp"),
		"GITHUB_OUTPUT=" + githubOutput,
		"GITHUB_STEP_SUMMARY=" + summary,
		"BINARY=" + filepath.Join(dir, "bin", "infraharvest"),
		"OUT=out",
		"FAKE_ARGS_LOG=" + argsLog,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+strings.ReplaceAll(v, "$DIR", dir))
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	outputs, _ := os.ReadFile(githubOutput)
	sum, _ := os.ReadFile(summary)
	args, _ := os.ReadFile(argsLog)
	return diagramResult{output: out.String(), outputs: string(outputs), summary: string(sum), args: string(args), dir: dir, err: runErr}
}

// draw runs the Draw the diagram step's script with the CLI installed, and a
// base graph when the test gives base.
func draw(t *testing.T, env map[string]string, base bool) diagramResult {
	t.Helper()
	all := map[string]string{"CLI": "$DIR/tmp/terragram.mjs"}
	files := map[string]string{}
	if base {
		all["BASE"] = "$DIR/tmp/base.json"
		files["tmp/base.json"] = `{"schema":"infraharvest/graph/v1"}`
	}
	for k, v := range env {
		all[k] = v
	}
	return runDiagram(t, "draw", all, files)
}

// warned says the run warned of something that has text in it, and didn't
// fail: the diagram is an extra.
func warned(t *testing.T, res diagramResult, text string) {
	t.Helper()
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	if !strings.Contains(res.output, "::warning::diagram: ") || !strings.Contains(res.output, text) {
		t.Errorf("want a warning with %q:\n%s", text, res.output)
	}
	if strings.Contains(res.outputs, "diagram-path") || strings.Contains(res.summary, "mermaid") {
		t.Errorf("a diagram was added:\n%s\n%s", res.outputs, res.summary)
	}
}

func TestDrawDrawsTheWholeEstateWithoutAnEarlierGraph(t *testing.T) {
	res := draw(t, nil, false)
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	if !strings.Contains(res.args, "--schema=graph --out-file="+res.dir+"/tmp/infraharvest-graph.json\n") {
		t.Errorf("the graph wasn't exported as the control plane step exports it, with no redaction:\n%s", res.args)
	}
	if !strings.Contains(res.args, "node "+res.dir+"/tmp/terragram.mjs graph "+res.dir+"/tmp/infraharvest-graph.json -f mermaid --title Imported estate\n") {
		t.Errorf("terragram graph wasn't run for the whole estate:\n%s", res.args)
	}
	if strings.Contains(res.args, "--base") || strings.Contains(res.args, "--delta") {
		t.Errorf("a base without an earlier graph:\n%s", res.args)
	}
	body := "## Diagram\n\n```mermaid\nflowchart TB\n  a[\"aws_vpc\"]\n```\n"
	if res.summary != body {
		t.Errorf("job summary = %q, want %q", res.summary, body)
	}
	written, err := os.ReadFile(filepath.Join(res.dir, "tmp", "infraharvest-diagram.md"))
	if err != nil || string(written) != body {
		t.Errorf("diagram file = %q, %v", written, err)
	}
	if want := "diagram-path=" + res.dir + "/tmp/infraharvest-diagram.md\n"; res.outputs != want {
		t.Errorf("outputs = %q, want %q", res.outputs, want)
	}
	// The graph document is the control plane's kind of data: not left behind.
	if _, err := os.Stat(filepath.Join(res.dir, "tmp", "infraharvest-graph.json")); err == nil {
		t.Error("the exported graph was left in the runner's temp directory")
	}
}

func TestDrawDrawsWhatChangedSinceTheEarlierGraph(t *testing.T) {
	res := draw(t, nil, true)
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	want := "-f mermaid --title Changes to the imported estate --base " + res.dir + "/tmp/base.json --delta\n"
	if !strings.Contains(res.args, want) {
		t.Errorf("terragram graph wasn't asked for the delta (%q):\n%s", want, res.args)
	}
	if !strings.Contains(res.summary, "```mermaid") {
		t.Errorf("job summary has no diagram:\n%s", res.summary)
	}
}

func TestDrawFallsBackToTheWholeEstateWhenTheEarlierGraphFails(t *testing.T) {
	res := draw(t, map[string]string{"FAKE_NODE_FAIL_BASE": "1"}, true)
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	if !strings.Contains(res.output, "::warning::diagram: couldn't draw what changed") {
		t.Errorf("no warning:\n%s", res.output)
	}
	if !strings.Contains(res.args, "--title Imported estate\n") || !strings.Contains(res.summary, "```mermaid") {
		t.Errorf("the whole estate wasn't drawn:\n%s\n%s", res.args, res.summary)
	}
	// What terragram logged when it failed isn't taken for the diagram.
	if strings.Contains(res.summary, "old graph") {
		t.Errorf("an error is in the diagram:\n%s", res.summary)
	}
}

func TestDrawRedactsAsTheControlPlaneStepDoes(t *testing.T) {
	res := draw(t, map[string]string{"HASH_IDS": "true", "HASH_KEY": "0123456789abcdef", "DROP_ADDRESSES": "true"}, false)
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	if !strings.Contains(res.args, "--schema=graph --out-file="+res.dir+"/tmp/infraharvest-graph.json --hash-ids --drop-addresses\n") {
		t.Errorf("the graph wasn't exported with the redaction flags:\n%s", res.args)
	}
	if !strings.Contains(res.args, "key=0123456789abcdef\n") {
		t.Errorf("the key didn't reach the export:\n%s", res.args)
	}
	if strings.Contains(res.output, "0123456789abcdef") || strings.Contains(res.summary, "0123456789abcdef") {
		t.Errorf("the key was printed:\n%s", res.output)
	}
	// Even with a base, which isn't redacted: keep doesn't make one then, so
	// nothing of it can reach the diagram.
	keep := runDiagram(t, "keep", map[string]string{"HASH_IDS": "true"}, map[string]string{earlier: "{}"})
	if keep.err != nil || keep.outputs != "" || !strings.Contains(keep.output, "::notice::diagram: the export redacts") {
		t.Errorf("keep with hash-ids: %v\noutputs %q\n%s", keep.err, keep.outputs, keep.output)
	}
	keep = runDiagram(t, "keep", map[string]string{"DROP_ADDRESSES": "true"}, map[string]string{earlier: "{}"})
	if keep.err != nil || keep.outputs != "" {
		t.Errorf("keep with drop-addresses: %v\noutputs %q\n%s", keep.err, keep.outputs, keep.output)
	}
}

func TestDrawUsesTheKeyFromTheEnvironment(t *testing.T) {
	res := draw(t, map[string]string{"HASH_IDS": "true", "INFRAHARVEST_EXPORT_KEY": "from-the-environment"}, false)
	if res.err != nil || !strings.Contains(res.args, "key=from-the-environment\n") {
		t.Errorf("%v\n%s", res.err, res.args)
	}
}

func TestDrawOnlyWarns(t *testing.T) {
	t.Run("no CLI", func(t *testing.T) {
		res := runDiagram(t, "draw", map[string]string{"CLI": ""}, nil)
		warned(t, res, "the Terragram CLI wasn't installed")
	})
	t.Run("missing CLI file", func(t *testing.T) {
		res := runDiagram(t, "draw", map[string]string{"CLI": "$DIR/nowhere.mjs"}, nil)
		warned(t, res, "the Terragram CLI wasn't installed")
	})
	t.Run("export fails", func(t *testing.T) {
		warned(t, draw(t, map[string]string{"FAKE_EXPORT_FAIL": "1"}, false), "infraharvest export (graph) failed: unknown document graph")
	})
	t.Run("terragram fails", func(t *testing.T) {
		warned(t, draw(t, map[string]string{"FAKE_NODE_FAIL": "1"}, true), "terragram graph exited with 1")
	})
	t.Run("terragram writes nothing", func(t *testing.T) {
		warned(t, draw(t, map[string]string{"FAKE_NODE_EMPTY": "1"}, false), "terragram graph wrote nothing")
	})
	t.Run("a code fence in the diagram", func(t *testing.T) {
		warned(t, draw(t, map[string]string{"FAKE_NODE_OUT": "flowchart TB\n  a[\"```\"]"}, false), "code fence")
	})
}

func TestDrawLeavesATooLargeDiagramOutWithANote(t *testing.T) {
	res := draw(t, map[string]string{"INFRAHARVEST_DIAGRAM_MAX": "10"}, false)
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	if strings.Contains(res.summary, "mermaid") || strings.Contains(res.summary, "flowchart") {
		t.Errorf("a diagram that is too large was added:\n%s", res.summary)
	}
	if !strings.Contains(res.summary, "has 28 characters, more than GitHub draws (about 10)") || strings.Count(res.summary, "\n") != 3 {
		t.Errorf("want one line saying so:\n%q", res.summary)
	}
	if !strings.Contains(res.outputs, "diagram-path=") {
		t.Errorf("the note isn't for the pull request:\n%s", res.outputs)
	}
}

func TestKeepCopiesTheEarlierGraph(t *testing.T) {
	res := runDiagram(t, "keep", nil, map[string]string{earlier: `{"earlier":true}`})
	if res.err != nil {
		t.Fatalf("failed: %v\n%s", res.err, res.output)
	}
	if want := "path=" + res.dir + "/tmp/infraharvest-graph-base.json\n"; res.outputs != want {
		t.Errorf("outputs = %q, want %q", res.outputs, want)
	}
	got, err := os.ReadFile(filepath.Join(res.dir, "tmp", "infraharvest-graph-base.json"))
	if err != nil || string(got) != `{"earlier":true}` {
		t.Errorf("copy = %q, %v", got, err)
	}
}

func TestKeepWithoutAnEarlierGraph(t *testing.T) {
	for name, files := range map[string]map[string]string{"none": nil, "empty": {earlier: ""}} {
		t.Run(name, func(t *testing.T) {
			res := runDiagram(t, "keep", nil, files)
			if res.err != nil || res.outputs != "" {
				t.Errorf("%v\noutputs %q\n%s", res.err, res.outputs, res.output)
			}
		})
	}
}

func TestKeepDoesNotFollowALink(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"secret":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The link is made in the run's directory, so the run is set up by hand.
	run := runDiagramWith(t, "keep", func(work string) error {
		if err := os.MkdirAll(filepath.Join(work, filepath.Dir(earlier)), 0o755); err != nil {
			return err
		}
		return os.Symlink(secret, filepath.Join(work, earlier))
	})
	if run.err != nil || run.outputs != "" || !strings.Contains(run.output, "::warning::diagram: "+earlier+" isn't a regular file") {
		t.Errorf("%v\noutputs %q\n%s", run.err, run.outputs, run.output)
	}
	if _, err := os.Stat(filepath.Join(run.dir, "tmp", "infraharvest-graph-base.json")); err == nil {
		t.Error("the file the link leads to was copied")
	}
}

// runDiagramWith runs diagram.sh in a directory that prepare has set up.
func runDiagramWith(t *testing.T, mode string, prepare func(dir string) error) diagramResult {
	t.Helper()
	dir := t.TempDir()
	if err := prepare(dir); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("diagram.sh")
	if err != nil {
		t.Fatal(err)
	}
	githubOutput := filepath.Join(dir, "github-output")
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", script, mode)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "RUNNER_TEMP=" + filepath.Join(dir, "tmp"), "GITHUB_OUTPUT=" + githubOutput, "OUT=out"}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	outputs, _ := os.ReadFile(githubOutput)
	return diagramResult{output: out.String(), outputs: string(outputs), dir: dir, err: runErr}
}

func TestPullRequestBodyCarriesTheDiagram(t *testing.T) {
	body := func(t *testing.T, diagram string, files map[string]string) (string, diagramResult) {
		t.Helper()
		dir := t.TempDir()
		temp := filepath.Join(dir, "tmp")
		if err := os.MkdirAll(temp, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(temp, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		script, err := filepath.Abs("pull-request-details.sh")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), "bash", script)
		cmd.Dir = dir
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "RUNNER_TEMP=" + temp, "GITHUB_OUTPUT=" + filepath.Join(dir, "github-output"),
			"IMPORT=true", "DRIFT=off", "OUT=out", "IMPORT_REPORT=" + filepath.Join(temp, "report.md"),
		}
		if diagram != "" {
			cmd.Env = append(cmd.Env, "DIAGRAM_FILE="+filepath.Join(temp, diagram))
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		runErr := cmd.Run()
		got, _ := os.ReadFile(filepath.Join(temp, "infraharvest-pull-request.md"))
		return string(got), diagramResult{output: out.String(), err: runErr}
	}
	t.Run("after the report", func(t *testing.T) {
		got, res := body(t, "diagram.md", map[string]string{"report.md": "# Report\n", "diagram.md": "## Diagram\n"})
		if res.err != nil || got != "# Report\n\n## Diagram\n" {
			t.Errorf("body = %q, %v\n%s", got, res.err, res.output)
		}
	})
	t.Run("without one", func(t *testing.T) {
		got, res := body(t, "", map[string]string{"report.md": "# Report\n"})
		if res.err != nil || got != "# Report\n" {
			t.Errorf("body = %q, %v\n%s", got, res.err, res.output)
		}
	})
	t.Run("too long for GitHub", func(t *testing.T) {
		got, res := body(t, "diagram.md", map[string]string{"report.md": strings.Repeat("x", 30000) + "\n", "diagram.md": strings.Repeat("y", 40000) + "\n"})
		if res.err != nil || strings.Contains(got, "y") || !strings.Contains(res.output, "::warning::diagram: the pull request's body would be longer") {
			t.Errorf("body has %d bytes, %v\n%s", len(got), res.err, res.output)
		}
	})
}

// stepOf returns the step of action.yml called name.
func stepOf(t *testing.T, name string) (script string, env map[string]string) {
	t.Helper()
	data, err := os.ReadFile("action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runs struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, step := range doc.Runs.Steps {
		if step.Name == name {
			return step.Run, step.Env
		}
	}
	t.Fatalf("action.yml has no step %q", name)
	return "", nil
}

// TestCheckInputsStep runs the Check inputs step's script of action.yml, with
// the variables its env has, so that what it refuses is tested as it is
// written there.
func TestCheckInputsStep(t *testing.T) {
	script, stepEnv := stepOf(t, "Check inputs")
	base := map[string]string{
		"VERSION": "v0.1.0", "IMPORT": "true", "DISCOVER": "false", "VERIFY": "false", "INCREMENTAL": "true",
		"ALLOW_PARTIAL": "false", "DRIFT": "off", "PULL_REQUEST": "false", "COMMIT": "false", "OUT": "infrastructure",
		"DROP_TAGS": "false", "HASH_IDS": "false", "DROP_ADDRESSES": "false", "OPTIONAL": "false",
		"SIGN_EVIDENCE": "false", "DIAGRAM": "false",
	}
	tests := []struct {
		name string
		env  map[string]string
		want string // empty: passes
	}{
		{"off", nil, ""},
		{"diagram", map[string]string{"DIAGRAM": "true"}, ""},
		{"diagram in a pull request", map[string]string{"DIAGRAM": "true", "PULL_REQUEST": "true"}, ""},
		{"diagram in a commit", map[string]string{"DIAGRAM": "true", "COMMIT": "true"}, ""},
		{"diagram with drift", map[string]string{"DIAGRAM": "true", "DRIFT": "report"}, ""},
		{"diagram with redaction", map[string]string{"DIAGRAM": "true", "HASH_IDS": "true", "HASH_KEY": "k", "DROP_ADDRESSES": "true"}, ""},
		{"diagram with the key in the environment", map[string]string{"DIAGRAM": "true", "HASH_IDS": "true", "INFRAHARVEST_EXPORT_KEY": "k"}, ""},
		{"diagram not true or false", map[string]string{"DIAGRAM": "yes"}, "diagram must be true or false, got 'yes'"},
		{"diagram with discover", map[string]string{"DIAGRAM": "true", "IMPORT": "false", "DISCOVER": "true"}, "discover leaves no imported estate to draw: set diagram to false"},
		{"diagram with verify", map[string]string{"DIAGRAM": "true", "IMPORT": "false", "VERIFY": "true"}, "verify leaves no imported estate to draw: set diagram to false"},
		{"diagram with discover and import", map[string]string{"DIAGRAM": "true", "DISCOVER": "true"}, "discover doesn't go with import"},
		{"diagram with drift only", map[string]string{"DIAGRAM": "true", "IMPORT": "false", "DRIFT": "report"}, "diagram draws an import's graph: set import to true"},
		{"diagram with hash-ids and no key", map[string]string{"DIAGRAM": "true", "HASH_IDS": "true"}, "control-plane-hash-ids needs a key"},
		{"diagram with a bad hash-ids", map[string]string{"DIAGRAM": "true", "HASH_IDS": "yes"}, "control-plane-hash-ids must be true or false, got 'yes'"},
		{"diagram with a bad drop-addresses", map[string]string{"DIAGRAM": "true", "DROP_ADDRESSES": "1"}, "control-plane-drop-addresses must be true or false, got '1'"},
		{"redaction without diagram is the control plane's to check", map[string]string{"HASH_IDS": "yes"}, ""},
	}
	actionPath, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := []string{"PATH=" + os.Getenv("PATH"), "GITHUB_ACTION_PATH=" + actionPath, "RUNNER_TEMP=" + t.TempDir()}
			vars := map[string]string{}
			for k, v := range base {
				vars[k] = v
			}
			for k, v := range tt.env {
				vars[k] = v
			}
			for k, v := range vars {
				if _, ok := stepEnv[k]; !ok && k != "INFRAHARVEST_EXPORT_KEY" {
					t.Fatalf("the Check inputs step has no variable %s", k)
				}
				env = append(env, k+"="+v)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
			cmd.Dir = t.TempDir()
			cmd.Env = env
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			if tt.want == "" {
				if err != nil {
					t.Errorf("failed: %v\n%s", err, out.String())
				}
				return
			}
			if err == nil || !strings.Contains(out.String(), "::error::") || !strings.Contains(out.String(), tt.want) {
				t.Errorf("want a failure mentioning %q: %v\n%s", tt.want, err, out.String())
			}
		})
	}
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package action

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// dispatchStep returns the script and the environment of the step called
// name in the job of the dispatch workflow.
func dispatchStep(t *testing.T, job, name string) (script string, env map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "ci", "github", "infraharvest-dispatch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, step := range doc.Jobs[job].Steps {
		if step.Name == name {
			return step.Run, step.Env
		}
	}
	t.Fatalf("job %s of the dispatch workflow has no step %q", job, name)
	return "", nil
}

// outputsOf reads the name=value lines of a step's GITHUB_OUTPUT file.
func outputsOf(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		name, value, _ := strings.Cut(line, "=")
		outputs[name] = value
	}
	return outputs
}

// The dispatch workflow's files step gives the decisions to a drift run and
// to no other: an import with drift off and drift-decisions set fails the
// action's input check. It refuses a file the branch linked elsewhere.
func TestDispatchFindsTheControlPlanesFiles(t *testing.T) {
	script, stepEnv := dispatchStep(t, "infraharvest", "Find the control plane's files")
	if _, ok := stepEnv["COMMAND"]; !ok {
		t.Fatal("the step has no COMMAND variable")
	}
	const inputs = ".infraharvest-inputs"
	for _, tc := range []struct {
		name, command string
		files         []string          // regular files the branch has
		links         map[string]string // files the branch links, to targets
		want          map[string]string // outputs; nil: the step fails
	}{
		{"import with both files", "import", []string{"selection.yaml", "drift.yaml"}, nil,
			map[string]string{"selection": inputs + "/selection.yaml", "decisions": "", "drift": "report"}},
		{"incremental with both files", "incremental", []string{"selection.yaml", "drift.yaml"}, nil,
			map[string]string{"selection": inputs + "/selection.yaml", "decisions": "", "drift": "report"}},
		{"import with decisions only", "import", []string{"drift.yaml"}, nil,
			map[string]string{"selection": "", "decisions": "", "drift": "report"}},
		{"drift with both files", "drift", []string{"selection.yaml", "drift.yaml"}, nil,
			map[string]string{"selection": inputs + "/selection.yaml", "decisions": inputs + "/drift.yaml", "drift": "accept"}},
		{"drift without decisions", "drift", []string{"selection.yaml"}, nil,
			map[string]string{"selection": inputs + "/selection.yaml", "decisions": "", "drift": "report"}},
		{"no files", "import", nil, nil, map[string]string{"selection": "", "decisions": "", "drift": "report"}},
		{"a linked selection", "import", []string{"drift.yaml"}, map[string]string{"selection.yaml": "/proc/self/environ"}, nil},
		{"a linked selection for drift", "drift", nil, map[string]string{"selection.yaml": "/proc/self/environ"}, nil},
		{"linked decisions", "drift", []string{"selection.yaml"}, map[string]string{"drift.yaml": "/proc/self/environ"}, nil},
		{"linked decisions for an import", "import", []string{"selection.yaml"}, map[string]string{"drift.yaml": "/proc/self/environ"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, inputs), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, inputs, f), []byte("x: y\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for f, target := range tc.links {
				if err := os.Symlink(target, filepath.Join(dir, inputs, f)); err != nil {
					t.Fatal(err)
				}
			}
			githubOutput := filepath.Join(t.TempDir(), "output")
			cmd := exec.CommandContext(t.Context(), "bash", "-eo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + githubOutput, "COMMAND=" + tc.command}
			out, err := cmd.CombinedOutput()
			if tc.want == nil {
				if err == nil || !strings.Contains(string(out), "::error::") || !strings.Contains(string(out), "symbolic link") {
					t.Errorf("want a failure naming the link: %v\n%s", err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("failed: %v\n%s", err, out)
			}
			got := outputsOf(t, githubOutput)
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("output %s is %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

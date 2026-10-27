// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/importer"
)

func TestDriftFlags(t *testing.T) {
	t.Parallel()
	cmd := newDriftCmd()
	for name, def := range map[string]string{
		"config":            "",
		"engine":            "terraform",
		"terraform-path":    "",
		"engine-timeout":    "1h0m0s",
		"format":            "text",
		"catalogue":         "",
		"parallel":          "1",
		"root":              "[]",
		"detailed-exitcode": "false",
		"force-unlock":      "false",
		"accept":            "",
		"keep":              "",
		"decisions":         "",
		"write-decisions":   "false",
	} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("drift has no --%s", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("--%s defaults to %q, want %q", name, f.DefValue, def)
		}
	}
}

func TestDriftRejectsBadFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"--format":    {"--format", "yaml"},
		"--parallel":  {"--parallel", "0"},
		"--engine":    {"--engine", "pulumi"},
		"--root":      {"--root", "aws/["},
		"--accept":    {"--accept", "everything"},
		"--keep":      {"--keep", "name=x"},
		"--decisions": {"--decisions", "no-such-drift.yaml"},
		"--catalogue": {"--catalogue", "no-such-catalogue"},
		"arguments":   {"a", "b"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newDriftCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append([]string{t.TempDir()}, args...))
			err := cmd.Execute()
			if err == nil {
				t.Fatal("drift ran")
			}
			if name != "arguments" && !strings.Contains(err.Error(), name) {
				t.Errorf("the error doesn't name %s: %v", name, err)
			}
		})
	}
}

func TestDriftWithoutRoots(t *testing.T) {
	cmd := newDriftCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{t.TempDir()})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no generated roots") {
		t.Errorf("got %v, want no generated roots", err)
	}
}

// --accept and --keep add to one list of rules, in the order given.
func TestDriftRuleFlagsKeepTheirOrder(t *testing.T) {
	t.Parallel()
	var rules []importer.DriftRule
	accept, keep := &driftRules{rules: &rules, accept: true}, &driftRules{rules: &rules}
	for _, set := range []struct {
		flag  *driftRules
		value string
	}{{accept, "all"}, {keep, "type=aws_security_group"}, {accept, "attribute=tags.*"}} {
		if err := set.flag.Set(set.value); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, r := range rules {
		got = append(got, fmt.Sprintf("%v %s", r.Accept, r))
	}
	if want := []string{"true all", "false type=aws_security_group", "true attribute=tags.*"}; !slices.Equal(got, want) {
		t.Errorf("rules %v, want %v", got, want)
	}
	if accept.String() != "all,attribute=tags.*" || keep.String() != "type=aws_security_group" {
		t.Errorf("--accept %q, --keep %q", accept.String(), keep.String())
	}
}

// --write-decisions takes no value, true or false, or the file to write.
func TestWriteDecisionsFlag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		on   bool
		path string
	}{
		{nil, false, ""},
		{[]string{"--write-decisions"}, true, ""},
		{[]string{"--write-decisions=false"}, false, ""},
		{[]string{"--write-decisions=decisions/drift.yaml"}, true, "decisions/drift.yaml"},
		{[]string{"--write-decisions=decisions/drift.yaml", "--write-decisions=true"}, true, ""},
	} {
		var on bool
		var path string
		cmd := newDriftCmd()
		cmd.Flags().Lookup("write-decisions").Value = &writeDecisions{on: &on, path: &path}
		if err := cmd.Flags().Parse(append(tc.args, "out")); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if on != tc.on || path != tc.path {
			t.Errorf("%v: on %v, file %q, want %v, %q", tc.args, on, path, tc.on, tc.path)
		}
		if args := cmd.Flags().Args(); !slices.Equal(args, []string{"out"}) {
			t.Errorf("%v: arguments %v, want the output directory", tc.args, args)
		}
	}
	if err := newDriftCmd().Flags().Parse([]string{"--write-decisions="}); err == nil {
		t.Error("--write-decisions= with no file: want an error")
	}
}

// --config applies the configuration file's settings drift has, as the
// import does, and leaves the import's others, such as services, to it.
func TestDriftConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		path := filepath.Join(dir, "infraharvest.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, tc := range map[string]struct{ config, want string }{
		"the engine applies":       {"version: 1\nsettings: { engine: pulumi, services: [vpc] }\n", `unknown --engine "pulumi"`},
		"the catalogue applies":    {"version: 1\nsettings: { catalogue: no-such-catalogue }\n", "--catalogue"},
		"a provider's engine":      {"version: 1\nproviders: { aws: { engine: tofu } }\n", "under settings"},
		"the command line decides": {"version: 1\nsettings: { engine: pulumi }\n", "no generated roots"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newDriftCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			args := []string{t.TempDir(), "--config", write(tc.config)}
			if name == "the command line decides" {
				args = append(args, "--engine=terraform")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %s", err, tc.want)
			}
		})
	}
}

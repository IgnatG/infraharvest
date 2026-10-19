// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/importer"
)

func TestDriftFlags(t *testing.T) {
	t.Parallel()
	cmd := newDriftCmd()
	for name, def := range map[string]string{
		"engine":            "terraform",
		"terraform-path":    "",
		"engine-timeout":    "1h0m0s",
		"format":            "text",
		"parallel":          "1",
		"root":              "[]",
		"detailed-exitcode": "false",
		"force-unlock":      "false",
		"accept":            "",
		"keep":              "",
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
	// No decisions file yet.
	for _, name := range []string{"decisions", "write-decisions"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("drift has --%s", name)
		}
	}
}

func TestDriftRejectsBadFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"--format":   {"--format", "yaml"},
		"--parallel": {"--parallel", "0"},
		"--engine":   {"--engine", "pulumi"},
		"--root":     {"--root", "aws/["},
		"--accept":   {"--accept", "everything"},
		"--keep":     {"--keep", "name=x"},
		"arguments":  {"a", "b"},
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

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
)

// One command, two Import calls (as the AWS command makes for global and
// regional resources): one report covering both, one exit code.
func TestEngineRunCombinesImports(t *testing.T) {
	out := t.TempDir()
	options := ImportOptions{PathOutput: out, AllowPartial: true}
	runE := withEngineRun(func(*cobra.Command, []string) error {
		for _, dir := range []string{"global", "us-east-1"} {
			activeRun.used = true
			activeRun.options = options
			activeRun.discovered["aws_vpc"]++
			result := &engine.Result{Imported: []engine.Import{{Type: "aws_vpc", Name: dir, ID: dir}}}
			if dir == "us-east-1" {
				result.Rejected = []engine.Rejection{{Address: "aws_vpc.extra", ID: "vpc-2", Errors: []string{"invalid"}}}
				activeRun.discovered["aws_vpc"]++
			}
			activeRun.addDirectory(filepath.Join(out, dir), nil, result, nil)
		}
		return nil
	})

	err := runE(nil, nil)

	if code := ExitCode(err); code != report.ExitPartial {
		t.Errorf("exit code %d, want %d (%v)", code, report.ExitPartial, err)
	}
	if activeRun != nil {
		t.Error("run still active after the command")
	}
	var coverage report.Report
	content, readErr := os.ReadFile(filepath.Join(out, report.Dir, "coverage.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(content, &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage.Directories) != 2 || coverage.Totals.Imported != 2 || coverage.Totals.LeftOut != 1 {
		t.Errorf("want both imports in one report, got %+v", coverage)
	}
}

// Legacy imports don't use the run: errors pass through, no report.
func TestEngineRunUnused(t *testing.T) {
	out := t.TempDir()
	want := errors.New("legacy failure")

	err := withEngineRun(func(*cobra.Command, []string) error {
		// Options are known, but no engine import used the run.
		activeRun.options = ImportOptions{PathOutput: out}
		return want
	})(nil, nil)

	if !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
	if _, statErr := os.Stat(filepath.Join(out, report.Dir)); statErr == nil {
		t.Error("report written for a legacy import")
	}
}

// A report that can't be written means the run couldn't complete, whatever
// the import itself did.
func TestEngineRunCouldNotWriteReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	partial := checkFailures([]error{errors.New("service sqs: access denied")}, true)
	for name, options := range map[string]ImportOptions{
		"import":   {PathOutput: out, AllowPartial: true},
		"discover": {Discover: true, Selection: filepath.Join(out, "selection.yaml"), AllowPartial: true},
	} {
		run := newEngineRun()
		run.used = true
		run.options = options

		err := run.finish(partial)

		if code := ExitCode(err); code != report.ExitCouldNotRun {
			t.Errorf("%s: exit code %d, want %d (%v)", name, code, report.ExitCouldNotRun, err)
		}
		if !errors.Is(err, partial) {
			t.Errorf("%s: the import's own error is lost: %v", name, err)
		}
	}
}

// fakeProviderCommand is a provider command as import and discover build them,
// with run in place of the provider's import.
func fakeProviderCommand(run func(ImportOptions) error) *cobra.Command {
	options := ImportOptions{}
	cmd := &cobra.Command{Use: "fake", SilenceUsage: true, SilenceErrors: true}
	cmd.RunE = withEngineRun(func(*cobra.Command, []string) error { return run(options) })
	cmd.Flags().String("config", "", "")
	baseProviderFlags(cmd.PersistentFlags(), &options, "", "")
	return cmd
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --resources can come from the configuration file, which is read after
// cobra checks required flags.
func TestResourcesFromConfig(t *testing.T) {
	config := writeConfig(t, "version: 1\nproviders:\n  fake:\n    resources: [vpc, s3]\n")
	var got ImportOptions
	cmd := fakeProviderCommand(func(o ImportOptions) error { got = o; return nil })
	cmd.SetArgs([]string{"--config", config})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"vpc", "s3"}; !slices.Equal(got.Resources, want) {
		t.Errorf("resources %v, want %v", got.Resources, want)
	}

	// Without --resources anywhere, the command says so.
	ran := false
	cmd = fakeProviderCommand(func(ImportOptions) error { ran = true; return nil })
	cmd.SetArgs([]string{"--engine", "terraform"})
	if err := cmd.Execute(); !errors.Is(err, errNoResources) {
		t.Errorf("got %v, want %v", err, errNoResources)
	}
	if ran {
		t.Error("the import ran without resources")
	}
}

// --path-pattern given as the legacy default is followed; left out, the
// Terraform engine lays roots out by account and region.
func TestDefaultPathPattern(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"legacy engine":         {[]string{"--resources", "vpc"}, DefaultPathPattern},
		"terraform engine":      {[]string{"--resources", "vpc", "--engine", "terraform"}, DefaultRootPathPattern},
		"explicit legacy":       {[]string{"--resources", "vpc", "--engine", "tofu", "--path-pattern", DefaultPathPattern}, DefaultPathPattern},
		"explicit other layout": {[]string{"--resources", "vpc", "--engine", "terraform", "--path-pattern", "{output}/{service}/"}, "{output}/{service}/"},
	} {
		t.Run(name, func(t *testing.T) {
			var got ImportOptions
			cmd := fakeProviderCommand(func(o ImportOptions) error { got = o; return nil })
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got.PathPattern != tc.want {
				t.Errorf("path pattern %q, want %q", got.PathPattern, tc.want)
			}
		})
	}

	// The configuration file counts as given, for the engine and the pattern.
	config := writeConfig(t, "version: 1\nsettings:\n  engine: terraform\nproviders:\n  fake:\n    resources: [vpc]\n")
	var got ImportOptions
	cmd := fakeProviderCommand(func(o ImportOptions) error { got = o; return nil })
	cmd.SetArgs([]string{"--config", config})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.PathPattern != DefaultRootPathPattern {
		t.Errorf("engine from the configuration file: path pattern %q, want %q", got.PathPattern, DefaultRootPathPattern)
	}
}

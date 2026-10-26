// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

func fakeProviderCommand(run func(importer.Options) error) *cobra.Command {
	options := importer.Options{}
	cmd := &cobra.Command{Use: "fake", SilenceUsage: true, SilenceErrors: true}
	cmd.RunE = withEngineRun(func(*cobra.Command, []string) error { return run(options) })
	cmd.Flags().String("config", "", "")
	providers.BaseFlags(cmd.PersistentFlags(), &options, "", "")
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

func TestServicesFromConfig(t *testing.T) {
	config := writeConfig(t, "version: 1\nproviders:\n  fake:\n    services: [vpc, s3]\n")
	var got importer.Options
	cmd := fakeProviderCommand(func(o importer.Options) error { got = o; return nil })
	cmd.SetArgs([]string{"--config", config})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"vpc", "s3"}; !slices.Equal(got.Services, want) {
		t.Errorf("services %v, want %v", got.Services, want)
	}
}

func TestLayout(t *testing.T) {
	byService := "{out}/{provider}/{service}/"
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default":      {[]string{"--services", "vpc"}, importer.DefaultLayout},
		"tofu":         {[]string{"--services", "vpc", "--engine", "tofu"}, importer.DefaultLayout},
		"by service":   {[]string{"--services", "vpc", "--layout", byService}, byService},
		"other layout": {[]string{"--services", "vpc", "--engine", "terraform", "--layout", "{out}/{service}/"}, "{out}/{service}/"},
	} {
		t.Run(name, func(t *testing.T) {
			var got importer.Options
			cmd := fakeProviderCommand(func(o importer.Options) error { got = o; return nil })
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got.Layout != tc.want {
				t.Errorf("layout %q, want %q", got.Layout, tc.want)
			}
		})
	}

	config := writeConfig(t, "version: 1\nsettings:\n  layout: \""+byService+"\"\nproviders:\n  fake:\n    services: [vpc]\n")
	var got importer.Options
	cmd := fakeProviderCommand(func(o importer.Options) error { got = o; return nil })
	cmd.SetArgs([]string{"--config", config})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.Layout != byService {
		t.Errorf("layout from the configuration file: %q, want %q", got.Layout, byService)
	}
}

// --regenerate is a flag of its own, and a setting of the configuration
// file; --resume doesn't set it.
func TestRegenerate(t *testing.T) {
	config := writeConfig(t, "version: 1\nsettings:\n  regenerate: true\nproviders:\n  fake:\n    services: [vpc]\n")
	for name, tc := range map[string]struct {
		args               []string
		regenerate, resume bool
	}{
		"flag":    {[]string{"--services", "vpc", "--regenerate"}, true, false},
		"resume":  {[]string{"--services", "vpc", "--resume"}, false, true},
		"both":    {[]string{"--services", "vpc", "--resume", "--regenerate"}, true, true},
		"setting": {[]string{"--config", config}, true, false},
		"neither": {[]string{"--services", "vpc"}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			var got importer.Options
			cmd := fakeProviderCommand(func(o importer.Options) error { got = o; return nil })
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got.Regenerate != tc.regenerate || got.Resume != tc.resume {
				t.Errorf("regenerate=%v resume=%v, want %v and %v", got.Regenerate, got.Resume, tc.regenerate, tc.resume)
			}
		})
	}
}

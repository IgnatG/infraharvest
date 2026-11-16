// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/report"
)

// TestOutDir checks that a command reading an import's output directory
// takes it as its argument or as --out, defaults to report.DefaultOut,
// and refuses both naming different directories.
func TestOutDir(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "default", want: report.DefaultOut},
		{name: "argument", args: []string{"roots"}, want: "roots"},
		{name: "flag", args: []string{"--out", "roots"}, want: "roots"},
		{name: "short flag", args: []string{"-o", "roots"}, want: "roots"},
		{name: "both, the same", args: []string{"--out", "roots", "roots"}, want: "roots"},
		{name: "both, different", args: []string{"--out", "a", "b"}, wantErr: `"b" as the argument and "a" as --out`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var flag, got string
			cmd := &cobra.Command{
				Use:  "test",
				Args: cobra.MaximumNArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					var err error
					got, err = outDir(cmd, args, flag)
					return err
				},
			}
			addOutFlag(cmd, &flag)
			cmd.SetArgs(tt.args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("output directory %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOutFlagOnReaders checks that every command that reads an import's
// output directory takes --out, as those that write it do.
func TestOutFlagOnReaders(t *testing.T) {
	root := NewCmdRoot()
	for _, name := range []string{"verify", "drift", "report", "export"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Flags().Lookup("out") == nil {
			t.Errorf("%s has no --out", name)
		}
	}
}

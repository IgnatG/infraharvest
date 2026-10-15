// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
)

// The CLI reference, docs/reference/cli.md, is generated from the commands
// and their flags. To rewrite it after changing them, without a local Go
// toolchain, run the remote workflow's golden task on the branch (see CONTRIBUTING.md),
// or locally:
//
//	go test ./internal/cli -run TestGoldenCLIReference -update
var update = flag.Bool("update", false, "rewrite the generated pages under docs/reference: cli.md and services.md")

// referenceProviders are the providers the reference documents.
var referenceProviders = []string{"aws", "azure", "google"}

const cliReference = "../../docs/reference/cli.md"

func TestGoldenCLIReference(t *testing.T) {
	for _, p := range referenceProviders {
		if providers.New(p) == nil {
			t.Skipf("built without %s", p)
		}
	}
	got := renderCLIReference(NewCmdRoot())
	if *update {
		if err := os.MkdirAll(filepath.Dir(cliReference), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cliReference, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(cliReference)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date: run go test ./internal/cli -run TestGoldenCLIReference -update, or the remote workflow's golden task with packages ./internal/cli", filepath.Base(cliReference))
	}
}

// renderCLIReference documents root's commands, and of the provider
// commands, those of referenceProviders.
func renderCLIReference(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("# Command-line reference\n\n")
	b.WriteString("<!-- Generated from the commands by TestGoldenCLIReference in internal/cli/clireference_test.go: don't edit. -->\n\n")
	b.WriteString("Every provider has the same `import` and `discover` flags, and flags of its own. This page lists AWS's, Azure's and Google Cloud's; `infraharvest import <provider> --help` lists any provider's.\n")
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		if c != root {
			writeCommand(&b, c)
		}
		subs := c.Commands()
		sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
		for _, sub := range subs {
			if (c.Name() == "import" || c.Name() == "discover") && !slices.Contains(referenceProviders, sub.Name()) {
				continue
			}
			walk(sub)
		}
	}
	walk(root)
	return b.String()
}

func writeCommand(b *strings.Builder, c *cobra.Command) {
	fmt.Fprintf(b, "\n## %s\n\n", c.CommandPath())
	if c.Short != "" {
		fmt.Fprintf(b, "%s\n\n", strings.TrimSpace(c.Short))
	}
	fmt.Fprintf(b, "```\n%s\n```\n", c.UseLine())
	var flags []*pflag.Flag
	seen := map[string]bool{}
	for _, set := range []*pflag.FlagSet{c.LocalFlags(), c.InheritedFlags()} {
		set.VisitAll(func(f *pflag.Flag) {
			if !f.Hidden && f.Name != "help" && !seen[f.Name] {
				seen[f.Name] = true
				flags = append(flags, f)
			}
		})
	}
	if len(flags) == 0 {
		return
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	b.WriteString("\n| Flag | Default | Description |\n|---|---|---|\n")
	for _, f := range flags {
		name := "`--" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "[]" && f.DefValue != "false" {
			def = "`" + f.DefValue + "`"
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n", name, def, cell(f.Usage))
	}
}

// cell makes text fit in a Markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(strings.TrimSpace(s))
}

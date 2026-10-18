// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/infraharvest/infraharvest-cli/internal/cli/providers"
)

// cliContract records the command-line contract: every command, how it
// takes arguments, and every flag with its shorthand, type, default and
// whether subcommands inherit it. Help text is left out, so that it can be
// rewritten without changing the contract. To rewrite the file after an
// intended change, run the remote workflow's golden task on the branch (see
// CONTRIBUTING.md), or locally:
//
//	go test ./internal/cli -run TestGoldenCLIContract -update
const cliContract = "testdata/cli_contract.golden.json"

// argsProbe is how many arguments, from none up, a command's argument
// validator is tried with.
const argsProbe = 3

// contractCommand is what the contract records of one command.
type contractCommand struct {
	Path    string   `json:"path"`
	Use     string   `json:"use"`
	Aliases []string `json:"aliases,omitempty"`
	Hidden  bool     `json:"hidden,omitempty"`
	RunE    bool     `json:"run_e"`
	Run     bool     `json:"run"`
	// Args is "unset" when the command leaves cobra to check its
	// arguments, else the numbers of arguments, from 0 to argsProbe, its
	// validator accepts, such as "0" or "0,1".
	Args          string         `json:"args"`
	SilenceUsage  bool           `json:"silence_usage"`
	SilenceErrors bool           `json:"silence_errors"`
	VersionFlag   bool           `json:"version_flag,omitempty"`
	Flags         []contractFlag `json:"flags,omitempty"`
}

// contractFlag is what the contract records of one flag, on the command
// that declares it.
type contractFlag struct {
	Name         string `json:"name"`
	Shorthand    string `json:"shorthand,omitempty"`
	Type         string `json:"type"`
	Default      string `json:"default"`
	NoOptDefault string `json:"no_opt_default,omitempty"`
	Persistent   bool   `json:"persistent"`
	Required     bool   `json:"required,omitempty"`
	Hidden       bool   `json:"hidden,omitempty"`
	Deprecated   bool   `json:"deprecated,omitempty"`
}

func TestGoldenCLIContract(t *testing.T) {
	for _, p := range referenceProviders {
		if providers.New(p) == nil {
			t.Skipf("built without %s", p)
		}
	}
	got, err := renderCLIContract(NewCmdRoot())
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(cliContract), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cliContract, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	hint := "if the change to the command-line contract is intended, run gh workflow run remote --ref <branch> -f task=golden -f packages=./internal/cli and commit the " + filepath.Base(cliContract) + " it writes (or locally: go test ./internal/cli -run TestGoldenCLIContract -update)"
	want, err := os.ReadFile(cliContract)
	if err != nil {
		t.Fatalf("%v: %s", err, hint)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("the command-line contract changed, %s:\n%s", hint, firstDifference(string(want), string(got)))
	}
}

// renderCLIContract returns the contract of root and every command under
// it, sorted by command path, as JSON.
func renderCLIContract(root *cobra.Command) ([]byte, error) {
	var commands []contractCommand
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		// Commands cobra adds itself when the command line runs.
		if c.Name() == "help" || c.Name() == "completion" {
			return
		}
		commands = append(commands, describeCommand(c, c == root))
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	sort.Slice(commands, func(i, j int) bool { return commands[i].Path < commands[j].Path })

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(commands); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func describeCommand(c *cobra.Command, isRoot bool) contractCommand {
	desc := contractCommand{
		Path:          c.CommandPath(),
		Use:           c.Use,
		Aliases:       slices.Clone(c.Aliases),
		Hidden:        c.Hidden,
		RunE:          c.RunE != nil,
		Run:           c.Run != nil,
		Args:          describeArgs(c),
		SilenceUsage:  c.SilenceUsage,
		SilenceErrors: c.SilenceErrors,
		VersionFlag:   isRoot && c.Version != "",
	}
	sort.Strings(desc.Aliases)
	add := func(persistent bool) func(*pflag.Flag) {
		return func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			desc.Flags = append(desc.Flags, contractFlag{
				Name:         f.Name,
				Shorthand:    f.Shorthand,
				Type:         f.Value.Type(),
				Default:      f.DefValue,
				NoOptDefault: f.NoOptDefVal,
				Persistent:   persistent,
				Required:     slices.Contains(f.Annotations[cobra.BashCompOneRequiredFlag], "true"),
				Hidden:       f.Hidden,
				Deprecated:   f.Deprecated != "",
			})
		}
	}
	c.PersistentFlags().VisitAll(add(true))
	c.LocalNonPersistentFlags().VisitAll(add(false))
	sort.Slice(desc.Flags, func(i, j int) bool { return desc.Flags[i].Name < desc.Flags[j].Name })
	return desc
}

// describeArgs says which numbers of arguments c's validator accepts.
func describeArgs(c *cobra.Command) string {
	if c.Args == nil {
		return "unset"
	}
	var accepted []string
	for n := 0; n <= argsProbe; n++ {
		args := make([]string, n)
		for i := range args {
			args[i] = "arg" + strconv.Itoa(i)
		}
		if c.Args(c, args) == nil {
			accepted = append(accepted, strconv.Itoa(n))
		}
	}
	if len(accepted) == 0 {
		return "none"
	}
	return strings.Join(accepted, ",")
}

// firstDifference shows where want and got first differ, line by line.
func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			return "line " + strconv.Itoa(i+1) + ":\n want: " + w + "\n  got: " + g
		}
	}
	return "(no line differs)"
}

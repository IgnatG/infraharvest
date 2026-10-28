// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providers

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// stubProvider is a provider of one service, queues, that lists nothing:
// enough for a command to check its flags against.
type stubProvider struct{}

func (*stubProvider) Init(context.Context, []string) error  { return nil }
func (*stubProvider) Lister(string) (harvest.Lister, error) { return nil, nil }
func (*stubProvider) Name() string                          { return "stub" }
func (*stubProvider) Profile() harvest.Profile              { return harvest.Profile{} }
func (*stubProvider) Services() map[string]harvest.Lister {
	return map[string]harvest.Lister{"queues": nil}
}
func (*stubProvider) ProviderBlock() map[string]any { return nil }

// stubCommand is what a definition of stubProvider's command saw run.
type stubCommand struct {
	// region is its own flag's value, as the checker saw it.
	region  string
	checked bool
	planned *importer.Options
	scopes  []string
	// imported are the scopes the plan was asked for the calls of.
	imported []string
	checkErr error
	planErr  error
}

// definition returns the definition of the stub command, with a --region
// flag of its own, whose checker and planner record what they are given.
// Its plan imports scopes with no calls, so running it reaches nothing.
func (s *stubCommand) definition() definition {
	return definition{
		name:        "stub",
		newProvider: func() harvest.Provider { return &stubProvider{} },
		short:       "stub",
		flags: func(flags *pflag.FlagSet, _ *importer.Options) (checker, planner) {
			flags.StringVar(&s.region, "region", "", "")
			check := func(importer.Options) error {
				s.checked = true
				return s.checkErr
			}
			plan := func(_ context.Context, options importer.Options) (plan, error) {
				s.planned = &options
				return plan{kind: "queue", scopes: s.scopes, calls: func(_ importer.Options, scope string) []call {
					s.imported = append(s.imported, scope)
					return nil
				}}, s.planErr
			}
			return check, plan
		},
	}
}

// A provider command checks the options every provider has, then its own
// flags, and with --check stops there; else it plans and runs the plan,
// with the options its flags and the command set: where it prints, whether
// --modules was given, and the managed state stores compiled in. It takes
// no arguments.
func TestCommand(t *testing.T) {
	errCheck := errors.New("check failed")
	errPlan := errors.New("plan failed")
	for _, tc := range []struct {
		name     string
		args     []string
		stub     stubCommand
		want     string // in the error; "" wants none
		checked  bool
		planned  bool
		modules  bool
		services []string
		imported []string
		// region is the command's own flag, as its checker saw it.
		region string
	}{
		{name: "no services", want: "--services is required"},
		{name: "unknown service", args: []string{"--all", "--services=topics"}, want: "stub has no service topics"},
		{name: "own check fails", args: []string{"--all", "--services=queues"}, stub: stubCommand{checkErr: errCheck}, want: errCheck.Error(), checked: true},
		{name: "check only", args: []string{"--all", "--services=queues", "--check", "--region=r1"}, checked: true, region: "r1"},
		{name: "plan fails", args: []string{"--all", "--services=queues"}, stub: stubCommand{planErr: errPlan}, want: errPlan.Error(), checked: true, planned: true, services: []string{"queues"}},
		{name: "runs", args: []string{"--all", "--services=*", "--modules=local"}, checked: true, planned: true, modules: true, services: []string{"*"}, imported: []string{""}},
		{name: "runs each scope", args: []string{"--all", "--services=queues"}, stub: stubCommand{scopes: []string{"q1", "q2"}}, checked: true, planned: true, services: []string{"queues"}, imported: []string{"q1", "q2"}},
		{name: "an argument", args: []string{"--services=queues", "extra"}, want: `unknown command "extra"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := tc.stub
			cmd, runE := stub.definition().command(importer.Options{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			// Whatever the run writes goes into a directory of the test's.
			cmd.SetArgs(append(slices.Clone(tc.args), "--out", t.TempDir()))
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			run := importer.NewRun()
			cmd.RunE = func(c *cobra.Command, args []string) error { return runE(c, args, run) }
			err := cmd.Execute()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one about %q", err, tc.want)
			}
			if stub.checked != tc.checked || (stub.planned != nil) != tc.planned {
				t.Errorf("checked %t, planned %t; want %t, %t", stub.checked, stub.planned != nil, tc.checked, tc.planned)
			}
			if stub.region != tc.region {
				t.Errorf("own flag: region %q, want %q", stub.region, tc.region)
			}
			if !slices.Equal(stub.imported, tc.imported) {
				t.Errorf("imported scopes %q, want %q", stub.imported, tc.imported)
			}
			if stub.planned == nil {
				return
			}
			o := stub.planned
			if o.Stdout != &out || o.ModulesChosen != tc.modules || o.ManagedStores.HCPTerraform == nil || !reflect.DeepEqual(o.Services, tc.services) {
				t.Errorf("planned with stdout %v, modules chosen %t, stores %+v, services %v", o.Stdout, o.ModulesChosen, o.ManagedStores, o.Services)
			}
		})
	}
}

// The command's flags are its own: two commands of one definition don't
// share options.
func TestCommandOptionsAreItsOwn(t *testing.T) {
	var stub stubCommand
	d := stub.definition()
	first, _ := d.command(importer.Options{})
	second, _ := d.command(importer.Options{})
	if err := first.PersistentFlags().Parse([]string{"--all", "--services=queues", "--out=a"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := second.PersistentFlags().GetStringSlice("services"); len(got) != 0 {
		t.Errorf("the second command's --services is %v", got)
	}
	if got, _ := second.PersistentFlags().GetString("out"); got == "a" {
		t.Error("the second command shares --out")
	}
}

// Several scopes need {account} in the layout to keep them apart; an empty
// layout leaves it to the provider.
func TestCheckScopesApart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pattern string
		want    string
	}{
		{"", ""},
		{"{out}/{provider}/{account}/{region}", ""},
		{"{out}/{provider}/{region}", `--layout "{out}/{provider}/{region}" needs {account} to keep several subscriptions apart`},
	} {
		got := ""
		if err := checkScopesApart(tc.pattern, "subscription"); err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

// appendMissing keeps the scopes given first, in their order, then adds
// those found it lacks, once each, and leaves the scopes given alone.
func TestAppendMissing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		scopes, found []string
		want          []string
	}{
		{"nothing", nil, nil, nil},
		{"found only", nil, []string{"b", "a"}, []string{"b", "a"}},
		{"given only", []string{"a"}, nil, []string{"a"}},
		{"overlap", []string{"c", "a"}, []string{"a", "b", "c", "d"}, []string{"c", "a", "b", "d"}},
		{"found twice", nil, []string{"a", "a"}, []string{"a"}},
	} {
		scopes := make([]string, len(tc.scopes), len(tc.scopes)+4)
		copy(scopes, tc.scopes)
		got := appendMissing(scopes, tc.found)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		if !slices.Equal(scopes, tc.scopes) {
			t.Errorf("%s: the scopes given changed to %v", tc.name, scopes)
		}
		// Its spare capacity isn't written into either.
		if spare := scopes[:cap(scopes)][len(scopes):]; slices.ContainsFunc(spare, func(s string) bool { return s != "" }) {
			t.Errorf("%s: wrote into the given slice's capacity: %v", tc.name, spare)
		}
	}
}

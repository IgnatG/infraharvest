// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/report"
)

// mergedDecisions is what fakeDrift writes as the merged decisions file.
const mergedDecisions = "version: 1\n# merged\n"

// fakeDrift prints sampleDrift as drift --format json does: with an
// --accept rule or a decisions file, the change of tags.Env is written
// into the code. It records the decisions file drift reads, and writes
// mergedDecisions with --write-decisions.
func (f *fakeRunner) fakeDrift(args []string) ([]byte, []byte, int, error) {
	if f.driftExit != 0 {
		return nil, []byte(f.driftStderr), f.driftExit, nil
	}
	out := args[len(args)-1]
	decisions, accepted, write := "", false, false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--accept="):
			accepted = true
		case strings.HasPrefix(a, "--decisions="):
			decisions, accepted = strings.TrimPrefix(a, "--decisions="), true
		case a == "--write-decisions":
			write = true
		}
	}
	if decisions != "" {
		content, err := os.ReadFile(decisions)
		if err != nil {
			return nil, nil, 2, err
		}
		f.decisions = append(f.decisions, string(content))
	}
	if write {
		path := decisions
		if path == "" {
			path = filepath.Join(out, "drift.yaml")
		}
		if err := os.WriteFile(path, []byte(mergedDecisions), 0o644); err != nil {
			return nil, nil, 2, err
		}
	}
	d := sampleDrift(accepted)
	content, err := json.Marshal(d)
	code := 0
	if d.DriftFound {
		code = report.ExitDrift
	}
	return content, nil, code, err
}

// sampleDrift is a root's drift: two tags the next apply would revert,
// the first written into the code once accepted, and an attribute the
// code absorbs.
func sampleDrift(accepted bool) *report.Drift {
	const root = "aws/1/eu-west-2"
	env := report.DriftChange{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: "changed", Decision: report.DriftKeep, Outcome: report.DriftKept}
	if accepted {
		env.Decision, env.Outcome = report.DriftAccept, report.DriftWritten
	}
	d := &report.Drift{
		Roots: []report.DriftRoot{{Path: root, Backend: "s3", Resources: 1}},
		Changes: []report.DriftChange{
			env,
			{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Owner", Before: "a", After: "b", Kind: "changed", Decision: report.DriftKeep, Outcome: report.DriftKept},
			{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "acceleration_status", Before: "", After: "Enabled", Kind: "changed", Decision: report.DriftKeep, Outcome: report.DriftAbsorbed},
		},
	}
	d.Finish()
	return d
}

// structured returns a result's structured content as a DriftResult.
func structured(t *testing.T, res *mcp.CallToolResult) DriftResult {
	t.Helper()
	content, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var r DriftResult
	if err := json.Unmarshal(content, &r); err != nil {
		t.Fatalf("%s: %v", content, err)
	}
	return r
}

// generatedOut makes an output directory under dir.
func generatedOut(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "generated")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return out
}

// Detection asks nobody: it changes no code.
func TestDrift(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)
	out := generatedOut(t, dir)

	res, text := call(t, session, "drift", map[string]any{"out": out, "roots": []string{"aws/*"}, "parallel": 2, "environments": []string{"prod", "dev"}})

	if res.IsError || !strings.Contains(text, "3 changes in the cloud") || !strings.Contains(text, "2 kept") {
		t.Errorf("result: %s", text)
	}
	want := []string{"drift", "--format=json", "--detailed-exitcode", "--root=aws/*", "--parallel=2", "--environment=prod,dev", "--", out}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
	r := structured(t, res)
	if r.ExitCode != report.ExitDrift || !r.DriftFound || r.Totals.Changes != 3 || r.Totals.Kept != 2 || r.Totals.Absorbed != 1 || len(r.Changes) != 3 {
		t.Errorf("structured result: %+v", r)
	}
	if r.Report != filepath.Join(out, report.Dir, report.DriftMarkdownFile) {
		t.Errorf("report: %s", r.Report)
	}
}

// When drift couldn't run, it printed no report to read.
func TestDriftCouldNotRun(t *testing.T) {
	runner := &fakeRunner{driftExit: report.ExitCouldNotRun, driftStderr: "no generated roots in generated\n"}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)

	res, text := call(t, session, "drift", map[string]any{"out": generatedOut(t, dir)})

	if !res.IsError || !strings.Contains(text, "no generated roots") {
		t.Errorf("want an error result with the command's output, got %s", text)
	}
}

// Accepting asks the user first, with what a check without the rules
// found, and only then writes into the code. The rules keep their order,
// on the command line and in the counts the user sees: the last that
// matches a change decides.
func TestDriftAcceptAfterTheUserConfirms(t *testing.T) {
	for name, tc := range map[string]struct {
		rules        []string
		flags        []string
		accept, keep string
	}{
		"keep last": {
			[]string{"accept attribute=tags.*", "keep attribute=tags.Owner"},
			[]string{"--accept=attribute=tags.*", "--keep=attribute=tags.Owner"},
			"accept 1 of them", "keep 1",
		},
		"accept last": {
			[]string{"keep attribute=tags.Owner", " attribute=tags.* "},
			[]string{"--keep=attribute=tags.Owner", "--accept=attribute=tags.*"},
			"accept 2 of them", "keep 0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{}
			var asked string
			dir := t.TempDir()
			session := connect(t, runner, dir, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = req.Params.Message
				return &mcp.ElicitResult{Action: "accept"}, nil
			})
			out := generatedOut(t, dir)

			res, text := call(t, session, "drift_accept", map[string]any{"out": out, "rules": tc.rules})

			if res.IsError || !strings.Contains(text, "1 accepted into the code") {
				t.Errorf("result: %s", text)
			}
			for _, want := range []string{fmt.Sprintf("roots in %q?", out), "found 2 changes", "the rules " + quoteAll(tc.rules), tc.accept, tc.keep, "never written", "never applies"} {
				if !strings.Contains(asked, want) {
					t.Errorf("confirmation %q misses %q", asked, want)
				}
			}
			base := []string{"drift", "--format=json", "--detailed-exitcode"}
			want := [][]string{
				append(slices.Clone(base), "--", out),
				append(append(slices.Clone(base), tc.flags...), "--", out),
			}
			if !slices.EqualFunc(runner.runs, want, slices.Equal) {
				t.Errorf("runs: %v, want %v", runner.runs, want)
			}
			if r := structured(t, res); r.Totals.Written != 1 || r.Totals.Kept != 1 {
				t.Errorf("structured result: %+v", r)
			}
		})
	}
}

// Nothing is written into the code without the user: not when they
// decline, nor when the client can't ask them, nor with an answer to
// another request.
func TestDriftAcceptNeedsTheUser(t *testing.T) {
	dir := t.TempDir()
	out := generatedOut(t, dir)
	decline := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	for name, tc := range map[string]struct {
		elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)
		want   string
		// checks counts the runs: the check before asking.
		checks int
	}{
		"declined":                {decline, "didn't confirm", 1},
		"a client that can't ask": {nil, "infraharvest drift --format=json --detailed-exitcode --accept=all --", 0},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{}
			session := connect(t, runner, dir, tc.elicit)

			res, text := call(t, session, "drift_accept", map[string]any{"out": out, "rules": []string{"accept all"}})

			if !res.IsError || !strings.Contains(text, tc.want) {
				t.Errorf("want an error result with %q, got %s", tc.want, text)
			}
			if len(runner.runs) != tc.checks || slices.ContainsFunc(runner.runs, func(args []string) bool { return slices.Contains(args, "--accept=all") }) {
				t.Errorf("accepted without confirmation: %v", runner.runs)
			}
		})
	}

	runner := &fakeRunner{}
	tools := newTools(runner, dir)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		InputResponses: mcp.InputResponseMap{driftConfirmation: &mcp.ElicitResult{Action: "accept"}},
		RequestState:   "a confirmation of another run",
	}}
	in := DriftAcceptInput{DriftInput: DriftInput{Out: out}, Rules: []string{"accept all"}}
	if res, _, err := tools.driftAccept(t.Context(), req, in); err != nil || res == nil || !res.IsError {
		t.Errorf("an answer to another request: got %+v, %v, want it refused", res, err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran without a matching confirmation: %v", runner.runs)
	}
}

// A decisions file needs the user too; the run reads the content they
// confirmed from a private copy, and write_decisions merges into the
// file itself.
func TestDriftAcceptDecisions(t *testing.T) {
	runner := &fakeRunner{}
	var asked string
	dir := t.TempDir()
	session := connect(t, runner, dir, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = req.Params.Message
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	out := generatedOut(t, dir)
	path := filepath.Join(dir, "drift.yaml")
	const decisions = "version: 1\ndefaults: { decision: accept }\n"
	if err := os.WriteFile(path, []byte(decisions), 0o644); err != nil {
		t.Fatal(err)
	}

	res, text := call(t, session, "drift_accept", map[string]any{"out": out, "decisions": path, "write_decisions": true})

	if res.IsError {
		t.Fatalf("result: %s", text)
	}
	for _, want := range []string{fmt.Sprintf("the decisions file %q accept 2 of them", path), "keep 0", fmt.Sprintf("into the decisions file %q", path)} {
		if !strings.Contains(asked, want) {
			t.Errorf("confirmation %q misses %q", asked, want)
		}
	}
	if len(runner.runs) != 2 || slices.ContainsFunc(runner.runs[0], func(a string) bool { return strings.HasPrefix(a, "--decisions=") || a == "--write-decisions" }) {
		t.Fatalf("runs: %v, want a check without the decisions, then the run", runner.runs)
	}
	var copied string
	for _, a := range runner.runs[1] {
		if p, ok := strings.CutPrefix(a, "--decisions="); ok {
			copied = p
		}
	}
	if copied == "" || copied == path || !slices.Contains(runner.runs[1], "--write-decisions") {
		t.Errorf("the run %v didn't read a private copy of %s", runner.runs[1], path)
	}
	if _, err := os.Stat(copied); err == nil {
		t.Errorf("the copy %s is left behind", copied)
	}
	if !slices.Equal(runner.decisions, []string{decisions}) {
		t.Errorf("drift read %q, want the confirmed content", runner.decisions)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != mergedDecisions {
		t.Errorf("the decisions file has %q, want the merged file (%v)", content, err)
	}
	if r := structured(t, res); r.Decisions != path {
		t.Errorf("structured result: %+v, want the decisions file", r)
	}
}

// A decisions file changed after the user was asked isn't applied.
func TestDriftAcceptChecksTheConfirmedDecisions(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	path := filepath.Join(dir, "drift.yaml")
	session := connect(t, runner, dir, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, os.WriteFile(path, []byte("version: 1\ndefaults: { decision: accept }\nrules: [ { keep: all } ]\n"), 0o644)
	})
	if err := os.WriteFile(path, []byte("version: 1\ndefaults: { decision: accept }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, text := call(t, session, "drift_accept", map[string]any{"out": generatedOut(t, dir), "decisions": path})

	if !res.IsError || !strings.Contains(text, "didn't confirm") {
		t.Errorf("want the run refused, got %s", text)
	}
	if len(runner.runs) != 1 || len(runner.decisions) != 0 {
		t.Errorf("applied decisions the user didn't confirm: %v", runner.runs)
	}
}

// Writing the decisions file alone, or keeping changes, writes no code,
// so nobody is asked.
func TestDriftAcceptWithoutCode(t *testing.T) {
	dir := t.TempDir()
	out := generatedOut(t, dir)
	for name, tc := range map[string]struct {
		args map[string]any
		flag string
	}{
		"write_decisions": {map[string]any{"out": out, "write_decisions": true}, "--write-decisions"},
		"keep":            {map[string]any{"out": out, "rules": []string{"keep type=aws_iam_*"}}, "--keep=type=aws_iam_*"},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{}
			session := connect(t, runner, dir, nil)

			res, text := call(t, session, "drift_accept", tc.args)

			if res.IsError {
				t.Fatalf("result: %s", text)
			}
			if len(runner.runs) != 1 || !slices.Contains(runner.runs[0], tc.flag) {
				t.Errorf("runs: %v, want one with %s", runner.runs, tc.flag)
			}
		})
	}
	if content, err := os.ReadFile(filepath.Join(out, "drift.yaml")); err != nil || string(content) != mergedDecisions {
		t.Errorf("drift.yaml has %q (%v)", content, err)
	}
}

// What the agent sends is checked before anything runs.
func TestDriftAcceptRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	out := generatedOut(t, dir)
	runner := &fakeRunner{}
	tools := newTools(runner, dir)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	for name, tc := range map[string]struct {
		in   DriftAcceptInput
		want string
	}{
		"nothing to do":         {DriftAcceptInput{DriftInput: DriftInput{Out: out}}, "call drift"},
		"no output directory":   {DriftAcceptInput{Rules: []string{"accept all"}}, "out is required"},
		"a rule of no field":    {DriftAcceptInput{DriftInput: DriftInput{Out: out}, Rules: []string{"accept name=x"}}, "attribute="},
		"a rule with a newline": {DriftAcceptInput{DriftInput: DriftInput{Out: out}, Rules: []string{"keep attribute=tags.*\nand accept all"}}, `rules "keep attribute=tags.*\nand accept all" has a control character`},
		"a root with a newline": {DriftAcceptInput{DriftInput: DriftInput{Out: out, Roots: []string{"aws/*\n"}}, Rules: []string{"accept all"}}, `roots "aws/*\n" has a control character`},
		"an empty root":         {DriftAcceptInput{DriftInput: DriftInput{Out: out, Roots: []string{""}}, Rules: []string{"accept all"}}, "roots has an empty value"},
		"an empty environment":  {DriftAcceptInput{DriftInput: DriftInput{Out: out, Environments: []string{"prod", ""}}, Rules: []string{"accept all"}}, "environments has an empty value"},
		// --environment takes a list: prod,dev would be two environments.
		"an environment with a comma": {DriftAcceptInput{DriftInput: DriftInput{Out: out, Environments: []string{"prod,dev"}}, Rules: []string{"accept all"}}, `environments "prod,dev" has a comma`},
		"a bad decisions file":        {DriftAcceptInput{DriftInput: DriftInput{Out: out}, Decisions: writeFile(t, dir, "bad.yaml", "version: 9\n")}, "version 9"},
		"no decisions file":           {DriftAcceptInput{DriftInput: DriftInput{Out: out}, Decisions: filepath.Join(dir, "missing.yaml")}, ""},
	} {
		if _, _, err := tools.driftAccept(t.Context(), req, tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, tc.want)
		}
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran with bad input: %v", runner.runs)
	}
}

// writeFile writes content into dir/name and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The output directory, the decisions file and the one write_decisions
// writes stay under the root, as the other tools' files do: the command
// line has no such limit.
func TestDriftFilesStayUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	for _, dir := range []string{filepath.Join(root, "generated"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inside := filepath.Join(root, "generated")
	decisions := writeFile(t, outside, "drift.yaml", "version: 1\n")
	runner := &fakeRunner{}
	tools := newTools(runner, root)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}

	if _, _, err := tools.drift(t.Context(), req, DriftInput{Out: outside}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("drift outside: got %v, want the output rejected", err)
	}
	for name, in := range map[string]DriftAcceptInput{
		"an output outside":               {DriftInput: DriftInput{Out: outside}, Rules: []string{"accept all"}},
		"decisions from outside":          {DriftInput: DriftInput{Out: inside}, Decisions: decisions},
		"decisions written outside":       {DriftInput: DriftInput{Out: inside}, Decisions: decisions, WriteDecisions: true},
		"decisions up from the root":      {DriftInput: DriftInput{Out: inside}, Decisions: filepath.Join(root, "..", "elsewhere", "drift.yaml")},
		"a relative path out of the root": {DriftInput: DriftInput{Out: filepath.Join("..", "elsewhere")}, WriteDecisions: true},
	} {
		if _, _, err := tools.driftAccept(t.Context(), req, in); err == nil || !strings.Contains(err.Error(), "must be under") {
			t.Errorf("%s: got %v, want the path rejected", name, err)
		}
	}

	// A link in the output directory that leads out of the root, where
	// write_decisions would write the decisions file.
	if err := os.Symlink(filepath.Join(outside, "drift.yaml"), filepath.Join(inside, "drift.yaml")); err != nil {
		t.Logf("no symbolic links here: %v", err)
	} else if _, _, err := tools.driftAccept(t.Context(), req, DriftAcceptInput{DriftInput: DriftInput{Out: inside}, WriteDecisions: true}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("write_decisions through a link: got %v, want it rejected", err)
	}
	if len(runner.runs) != 0 {
		t.Errorf("ran with files outside the root: %v", runner.runs)
	}
}

// A result lists the first changes only, and counts the stale decisions.
func TestDriftResultBoundsTheChanges(t *testing.T) {
	d := &report.Drift{Roots: []report.DriftRoot{{Path: "aws/1/eu-west-2"}, {Path: "aws/2/eu-west-2", Error: "remote backends aren't supported"}}}
	for i := range maxDriftChanges + 50 {
		d.Changes = append(d.Changes, report.DriftChange{Root: "aws/1/eu-west-2", Address: fmt.Sprintf("aws_s3_bucket.b%03d", i), Attribute: "tags.Env", Outcome: report.DriftKept, DecisionStale: i%100 == 0})
	}
	d.Finish()

	r := driftResult(d, report.ExitCouldNotRun, "generated")
	text := driftText(r)

	if len(r.Changes) != maxDriftChanges || r.MoreChanges != 50 || r.StaleDecisions != 3 || r.Totals.Kept != maxDriftChanges+50 {
		t.Errorf("result: %d changes, %d more, %d stale, totals %+v", len(r.Changes), r.MoreChanges, r.StaleDecisions, r.Totals)
	}
	if !slices.Equal(r.FailedRoots, []string{"aws/2/eu-west-2: remote backends aren't supported"}) {
		t.Errorf("failed roots: %v", r.FailedRoots)
	}
	for _, want := range []string{"Checked 1 roots (1 couldn't be)", "leaves out 50 more", "3 changes changed again"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q misses %q", text, want)
		}
	}
}

// The catalogue directory is passed on as --catalogue, and stays under
// the root as the other files do.
func TestDriftCatalogue(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)
	out := generatedOut(t, dir)
	catalogue := filepath.Join(dir, "catalogue")
	if err := os.MkdirAll(catalogue, 0o755); err != nil {
		t.Fatal(err)
	}

	if res, text := call(t, session, "drift", map[string]any{"out": out, "catalogue": catalogue}); res.IsError {
		t.Fatalf("result: %s", text)
	}
	want := []string{"drift", "--format=json", "--detailed-exitcode", "--catalogue=" + catalogue, "--", out}
	if len(runner.runs) != 1 || !slices.Equal(runner.runs[0], want) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}

	tools := newTools(runner, dir)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	if _, _, err := tools.drift(t.Context(), req, DriftInput{Out: out, Catalogue: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "must be under") {
		t.Errorf("a catalogue outside the root: got %v, want it rejected", err)
	}
	if len(runner.runs) != 1 {
		t.Errorf("ran with a catalogue outside the root: %v", runner.runs[1:])
	}
}

// engine_timeout and force_unlock set --engine-timeout and --force-unlock,
// for drift_accept too.
func TestDriftEngineTimeoutAndForceUnlock(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	session := connect(t, runner, dir, nil)
	out := generatedOut(t, dir)

	if res, text := call(t, session, "drift", map[string]any{"out": out, "engine_timeout": "90m", "force_unlock": true}); res.IsError {
		t.Fatalf("result: %s", text)
	}
	if res, text := call(t, session, "drift_accept", map[string]any{"out": out, "engine_timeout": "0", "rules": []string{"keep all"}}); res.IsError {
		t.Fatalf("result: %s", text)
	}
	want := [][]string{
		{"drift", "--format=json", "--detailed-exitcode", "--engine-timeout=90m", "--force-unlock", "--", out},
		{"drift", "--format=json", "--detailed-exitcode", "--engine-timeout=0", "--keep=all", "--", out},
	}
	if !slices.EqualFunc(runner.runs, want, slices.Equal) {
		t.Errorf("runs: %v, want %v", runner.runs, want)
	}
}

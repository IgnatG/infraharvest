// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftExitHelp says what the drift tools' exit codes mean, for their
// descriptions: they run drift with --detailed-exitcode.
const driftExitHelp = "Exit code 0 means no drift is left, 4 that changes are left that the next apply reverts or that can't be assessed, 1 that an accepted change failed verification, and 2 that a root couldn't be checked."

// DriftInput is the drift tool's input.
type DriftInput struct {
	Out    string `json:"out" jsonschema:"the output directory of an import, whose roots were applied"`
	Engine string `json:"engine,omitempty" flag:"engine" jsonschema:"terraform (the default) or tofu"`
	// Config is --config, checked as the other paths are.
	Config string `json:"config,omitempty" jsonschema:"a configuration file, as --config takes it, that sets engine or engine-timeout under settings, which apply where not given here; a file that sets anything else, such as the import's scope, backend or environments, is refused"`
	// Roots are --root patterns.
	Roots []string `json:"roots,omitempty" flag:"root,each" jsonschema:"check only the roots whose path in the output directory, or a directory it is in, matches one of these patterns, such as aws/*/eu-west-2 (default: every root)"`
	// Parallel is --parallel, 0 for its default.
	Parallel int `json:"parallel,omitempty" flag:"parallel" jsonschema:"how many roots to check at once (default 1); each runs its own Terraform"`
	// Environments are --environment names.
	Environments []string `json:"environments,omitempty" flag:"environment" jsonschema:"for a root environments share, the environments to check it as, each with its backend settings and var files, with the credentials the server runs with, or all for every one under its vars/, if they reach every one; roots of more than one environment need it"`
	// Catalogue is --catalogue.
	Catalogue string `json:"catalogue,omitempty" jsonschema:"the directory of catalogue files the import used, which add to or override the built-in catalogue: the arguments it says are kept only in state aren't drift"`
	// EngineTimeout is --engine-timeout, "" for its default.
	EngineTimeout string `json:"engine_timeout,omitempty" flag:"engine-timeout" jsonschema:"the longest time one Terraform or OpenTofu command, such as the refresh of one root, may run, as a duration such as 30m or 2h (default 1h; 0 for no limit)"`
	// ForceUnlock is --force-unlock.
	ForceUnlock bool `json:"force_unlock,omitempty" flag:"force-unlock" jsonschema:"check even when another run holds the output directory's lock, as when that run is stuck, or when the file system can't lock"`
}

// DriftAcceptInput is the drift_accept tool's input.
type DriftAcceptInput struct {
	DriftInput
	// Rules are accept and keep rules, in order, each as a line of drift's
	// --rules file takes it (see driftpolicy.ParseRuleLine): they become
	// --accept and --keep flags in that order, so that the last that
	// matches a change decides, as on the command line.
	Rules []string `json:"rules,omitempty" jsonschema:"rules of the changes to accept into the code, or to keep the code's values of, which the next apply puts back, in order, the last that matches a change deciding: each accept or keep, then all, or attribute=, type=, address= or root= with a pattern in which * matches any text, such as accept attribute=tags.* or keep type=aws_security_group; a rule alone is accepted"`
	// Decisions is --decisions.
	Decisions string `json:"decisions,omitempty" jsonschema:"a decisions file to decide the changes by, such as one write_decisions wrote and the user edited: each change's entry, else its rules and defaults; rules decide after it"`
	// WriteDecisions is --write-decisions.
	WriteDecisions bool `json:"write_decisions,omitempty" flag:"write-decisions" jsonschema:"write the run's changes into the decisions file, decisions or drift.yaml in the output directory: entries whose change is unchanged keep their decisions, new changes and those that changed again are undecided, and changes no longer found are dropped"`
}

// DriftResult is what a drift tool found and did.
type DriftResult struct {
	ExitCode int `json:"exit_code"`
	// DriftFound says whether changes are left that the next apply
	// reverts, or that can't be assessed.
	DriftFound bool `json:"drift_found"`
	// Totals count the roots, and the changes by what became of them:
	// written into the code, kept, needing a hand edit (manual), and so
	// on.
	Totals report.DriftTotals `json:"totals"`
	// StaleDecisions counts the changes whose entry in the decisions file
	// was decided for other values, so that it wasn't applied.
	StaleDecisions int `json:"stale_decisions,omitempty"`
	// FailedRoots are the roots that couldn't be checked, with why.
	FailedRoots []string `json:"failed_roots,omitempty"`
	// Changes are the first changes, up to maxDriftChanges; MoreChanges
	// counts those left out, which the report has.
	Changes     []report.DriftChange `json:"changes"`
	MoreChanges int                  `json:"more_changes,omitempty"`
	// Report is drift.md; drift.json is beside it.
	Report string `json:"report"`
	// Decisions is the decisions file write_decisions wrote.
	Decisions string `json:"decisions,omitempty"`
}

// maxDriftChanges is how many changes a result lists: an estate's tags
// changed in the console can be thousands, too many for an agent's
// context, and the report has them all.
const maxDriftChanges = 200

// driftConfirmation names the drift_accept tool's request for the user's
// confirmation.
const driftConfirmation = "confirm_drift"

// driftArgs checks the paths of in and returns the arguments of drift
// without the flags of a tool's input (see flagArgs), nor the output
// directory, which goes last, with the configuration file in, if any,
// which the caller removes (see loadConfig), checked for tool, the drift
// tool calling it. in.Out is set to the path to use.
func (t *tools) driftArgs(tool string, in *DriftInput) ([]string, *toolConfig, error) {
	if in.Out == "" {
		return nil, nil, errors.New("out is required")
	}
	out, err := t.within("out", in.Out)
	if err != nil {
		return nil, nil, err
	}
	in.Out = out
	// drift initialises the roots, stages copies of them, and writes the
	// report and edits there.
	if err := t.linksStayUnder("out", in.Out); err != nil {
		return nil, nil, err
	}
	args := []string{"drift", "--format=json", "--detailed-exitcode"}
	if in.Catalogue != "" {
		catalogue, err := t.within("catalogue", in.Catalogue)
		if err != nil {
			return nil, nil, err
		}
		// drift reads every file under it.
		if err := t.linksStayUnder("catalogue", catalogue); err != nil {
			return nil, nil, err
		}
		args = append(args, "--catalogue="+catalogue)
	}
	config, err := t.loadConfig(&in.Config, configUse{tool: tool})
	if err != nil {
		return nil, nil, err
	}
	return append(args, config.flag()...), config, nil
}

func (t *tools) drift(ctx context.Context, _ *mcp.CallToolRequest, in DriftInput) (*mcp.CallToolResult, *DriftResult, error) {
	args, config, err := t.driftArgs("drift", &in)
	if err != nil {
		return nil, nil, err
	}
	defer config.remove()
	flags, err := flagArgs(in)
	if err != nil {
		return nil, nil, err
	}
	args = append(args, flags...)
	d, code, failed, err := t.runDrift(ctx, config.run(append(args, "--", in.Out)), "drift")
	if err != nil || failed != nil {
		return failed, nil, err
	}
	result := driftResult(d, code, in.Out)
	return textResult(driftText(result)), result, nil
}

func (t *tools) driftAccept(ctx context.Context, req *mcp.CallToolRequest, in DriftAcceptInput) (*mcp.CallToolResult, *DriftResult, error) {
	args, config, err := t.driftArgs("drift_accept", &in.DriftInput)
	if err != nil {
		return nil, nil, err
	}
	defer config.remove()
	// The flags of the check without the rules and the decisions file,
	// whose changes the confirmation counts, and then with them.
	detectFlags, err := flagArgs(in.DriftInput)
	if err != nil {
		return nil, nil, err
	}
	flags, err := flagArgs(in)
	if err != nil {
		return nil, nil, err
	}
	// Parsed here, for the confirmation to count what they accept, and
	// passed on as --accept and --keep flags in their order.
	rules, ruleFlags, err := parseDriftRules(in.Rules)
	if err != nil {
		return nil, nil, err
	}
	detect := append(slices.Clone(args), detectFlags...)
	args = append(append(args, flags...), ruleFlags...)
	if len(rules) == 0 && in.Decisions == "" && !in.WriteDecisions {
		return nil, nil, errors.New("set rules, decisions or write_decisions; to only check, call drift")
	}
	target := filepath.Join(in.Out, driftpolicy.DefaultDecisionsFile)
	var content []byte
	var decisions *driftpolicy.Decisions
	if in.Decisions != "" {
		// Read once: the user confirms these bytes, as for an import's
		// selection file.
		if in.Decisions, content, err = t.readWithin("decisions", in.Decisions); err != nil {
			return nil, nil, err
		}
		if decisions, err = driftpolicy.ParseDecisions(in.Decisions, content); err != nil {
			return nil, nil, err
		}
		target = in.Decisions
	}
	if in.WriteDecisions {
		if _, err := t.within("decisions", target); err != nil {
			return nil, nil, err
		}
	}
	if !slices.ContainsFunc(rules, func(r driftpolicy.Rule) bool { return r.Accept }) && in.Decisions == "" {
		// Only keep rules, or only the decisions file to write: no code is
		// written, so there is nothing for the user to confirm.
		return t.finishDrift(ctx, config.run(append(args, "--", in.Out)), in, target, nil, nil)
	}
	shown := slices.Clone(args)
	if in.Decisions != "" {
		shown = append(shown, "--decisions="+in.Decisions)
	}
	shown = append(shown, "--", in.Out)

	// A person confirms what is written into the code, as for an import.
	state := confirmationState(shown, content, config.sum())
	if len(req.Params.InputResponses) == 0 {
		if !canAsk(req.Session) {
			return errorResult(fmt.Sprintf("The user has to confirm taking changes in the cloud into the code, and this client can't ask them. Ask the user to run it themselves:\n\ninfraharvest %s", shellJoin(shown))), nil, nil
		}
		// The counts to confirm come from a run without the rules and the
		// decisions file, which changes no code.
		d, _, failed, err := t.runDrift(ctx, config.run(append(detect, "--", in.Out)), "The check before asking the user")
		if err != nil || failed != nil {
			return failed, nil, err
		}
		message := driftConfirmationMessage(in, target, config, d, driftpolicy.Decider{Decisions: decisions, Rules: rules}.Preview(d.Changes))
		t.issue(state)
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{driftConfirmation: &mcp.ElicitParams{
				Message:         message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: state,
		}, nil, nil
	}
	answer, ok := req.Params.InputResponses[driftConfirmation].(*mcp.ElicitResult)
	// Only an answer to a request this server made counts, and only once.
	if !ok || answer.Action != "accept" || req.Params.RequestState != state || !t.redeem(state) {
		return errorResult("The user didn't confirm taking changes in the cloud into the code, so the code wasn't changed."), nil, nil
	}
	if in.Decisions == "" {
		return t.finishDrift(ctx, config.run(shown), in, target, nil, nil)
	}
	snapshot, err := newSnapshot(filepath.Base(in.Decisions), content)
	if err != nil {
		return nil, nil, err
	}
	defer snapshot.remove()
	return t.finishDrift(ctx, config.run(append(args, "--decisions="+snapshot.path, "--", in.Out)), in, target, snapshot, content)
}

// parseDriftRules parses rules, accept and keep rules in order, with the
// parser drift's --rules file has, and returns them with the --accept and
// --keep flags that pass them on in that order. A rule with a control
// character, such as a newline, which could make the confirmation the
// user is shown say something else, is rejected.
func parseDriftRules(rules []string) ([]driftpolicy.Rule, []string, error) {
	parsed := make([]driftpolicy.Rule, 0, len(rules))
	flags := make([]string, 0, len(rules))
	for _, s := range rules {
		if strings.ContainsFunc(s, unicode.IsControl) {
			return nil, nil, fmt.Errorf("rules %q has a control character", s)
		}
		r, err := driftpolicy.ParseRuleLine(s)
		if err != nil {
			return nil, nil, err
		}
		flag := "--keep="
		if r.Accept {
			flag = "--accept="
		}
		parsed = append(parsed, r)
		flags = append(flags, flag+r.String())
	}
	return parsed, flags, nil
}

// driftConfirmationMessage asks the user to confirm in, saying how many
// of the changes d, a run without the rules and the decisions file,
// found that they would accept, as p counts them, and what config, the
// configuration file, if any, sets.
func driftConfirmationMessage(in DriftAcceptInput, target string, config *toolConfig, d *report.Drift, p driftpolicy.Preview) string {
	var by []string
	if in.Decisions != "" {
		by = append(by, fmt.Sprintf("the decisions file %q", in.Decisions))
	}
	if len(in.Rules) > 0 {
		by = append(by, "the rules "+quoteAll(in.Rules)+", the last that matches a change deciding,")
	}
	message := fmt.Sprintf("Take changes in the cloud into the Terraform code of the roots in %q? A check just now found %d changes the next apply would revert: %s accept %d of them, written into the roots' files, and keep %d, which the next apply puts back.",
		in.Out, p.Accept+p.Keep, strings.Join(by, ", then "), p.Accept, p.Keep)
	if p.Stale > 0 {
		message += fmt.Sprintf(" %d entries of the decisions file were decided for other values, as the cloud changed again since, so they aren't applied.", p.Stale)
	}
	if d.Totals.Failed > 0 {
		message += fmt.Sprintf(" %d roots couldn't be checked.", d.Totals.Failed)
	}
	message += " An edit is kept only once the plan of the edited code shows the resource taking it and nothing else changing; secrets are never written, and changes that take a hand edit are reported instead."
	message += config.message()
	if in.WriteDecisions {
		message += fmt.Sprintf(" The run's changes are then written into the decisions file %q.", target)
	}
	return message + " infraharvest reads the cloud and the roots' state and writes files; it never applies anything, nor changes the state."
}

// quoteAll quotes each of values, joined with commas.
func quoteAll(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}

// finishDrift runs drift_accept's drift with args. With copied, the
// private copy of the decisions file whose content the user confirmed,
// that write_decisions writes into, the merged file is copied back to
// in.Decisions, unless that changed meanwhile.
func (t *tools) finishDrift(ctx context.Context, args []string, in DriftAcceptInput, target string, copied *snapshot, content []byte) (*mcp.CallToolResult, *DriftResult, error) {
	d, code, failed, err := t.runDrift(ctx, args, "drift")
	if err != nil || failed != nil {
		return failed, nil, err
	}
	result := driftResult(d, code, in.Out)
	text := driftText(result)
	if in.WriteDecisions {
		if err := copyBackDecisions(copied, target, content); err != nil {
			text += fmt.Sprintf(" The decisions weren't written to %s: %v.", target, err)
		} else {
			result.Decisions = target
			text += fmt.Sprintf(" The decisions file is %s: decide its undecided entries, then call drift_accept with it as decisions.", target)
		}
	}
	return textResult(text), result, nil
}

// copyBackDecisions copies the decisions file drift merged into copied
// to target, unless target no longer has content, the content the user
// confirmed. Without copied, drift wrote target itself.
func copyBackDecisions(copied *snapshot, target string, content []byte) error {
	if copied == nil {
		return nil
	}
	merged, err := os.ReadFile(copied.path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, content) {
		return errors.New("it changed while drift ran")
	}
	if bytes.Equal(merged, content) {
		return nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(target, merged, info.Mode().Perm())
}

// runDrift runs drift with args and reads the drift.json it prints. When
// it printed none, as when it couldn't run, it returns an error result
// that says what stopped it; what names the run there.
func (t *tools) runDrift(ctx context.Context, args []string, what string) (*report.Drift, int, *mcp.CallToolResult, error) {
	stdout, stderr, code, err := t.run(ctx, args)
	if err != nil {
		return nil, 0, nil, err
	}
	d, err := decodeDrift(stdout)
	if err != nil {
		if stopped(code) {
			return nil, code, errorResult(fmt.Sprintf("%s was stopped (exit code %d):\n%s", what, code, tail(stderr))), nil
		}
		return nil, code, errorResult(fmt.Sprintf("%s couldn't run (exit code %d):\n%s", what, code, tail(stderr))), nil
	}
	return d, code, nil, nil
}

// decodeDrift reads drift.json. Numbers are kept as they are written, so
// that the fingerprints of the changes' values are those drift took.
func decodeDrift(content []byte) (*report.Drift, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var d report.Drift
	if err := decoder.Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// driftResult is d as a drift tool returns it, from a run that exited
// with code on the output directory out.
func driftResult(d *report.Drift, code int, out string) *DriftResult {
	result := &DriftResult{
		ExitCode:   code,
		DriftFound: d.DriftFound,
		Totals:     d.Totals,
		Changes:    []report.DriftChange{},
		Report:     filepath.Join(out, report.Dir, report.DriftMarkdownFile),
	}
	for _, r := range d.Roots {
		if r.Error != "" {
			result.FailedRoots = append(result.FailedRoots, r.Name()+": "+r.Error)
		}
	}
	for _, c := range d.Changes {
		if c.DecisionStale {
			result.StaleDecisions++
		}
		if len(result.Changes) == maxDriftChanges {
			result.MoreChanges++
			continue
		}
		result.Changes = append(result.Changes, c)
	}
	return result
}

// driftText summarises result.
func driftText(result *DriftResult) string {
	t, code := result.Totals, result.ExitCode
	text := fmt.Sprintf("Checked %d roots (%d couldn't be): %d changes in the cloud since they were applied; %d accepted into the code, %d kept, which the next apply reverts, %d need a hand edit, %d rejected by the plan, %d rejected by a check, %d secrets, %d not assessable, and %d the code absorbs (exit code %d). The report is in %s.",
		t.Roots-t.Failed, t.Failed, t.Changes, t.Written, t.Kept, t.Manual, t.RejectedByPlan, t.RejectedByCheck, t.Secret, t.NotAssessable, t.Absorbed, code, result.Report)
	if result.MoreChanges > 0 {
		text += fmt.Sprintf(" The result lists the first %d changes and leaves out %d more: the report has them all.", maxDriftChanges, result.MoreChanges)
	}
	if result.StaleDecisions > 0 {
		text += fmt.Sprintf(" %d changes changed again since their entries in the decisions file were decided, so those weren't applied: decide them again.", result.StaleDecisions)
	}
	if stopped(code) {
		text = "drift was stopped before it finished. " + text
	}
	return text
}

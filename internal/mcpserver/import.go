// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/report"
)

// ImportInput is the import tool's input.
type ImportInput struct {
	Scope
	Selection string `json:"selection" jsonschema:"the selection file to import, from discover"`
	Out       string `json:"out" jsonschema:"the directory to write the configuration and report to"`
	// Config is --config, checked as the other paths are.
	Config string `json:"config,omitempty" jsonschema:"a configuration file, as --config takes it, which sets what the other parameters don't, such as the services and regions, and the state backend of the generated roots"`
	Engine string `json:"engine,omitempty" flag:"engine" jsonschema:"terraform (the default) or tofu"`
	// Incremental adds to the roots Out already has (--incremental).
	Incremental bool `json:"incremental,omitempty" flag:"incremental" jsonschema:"add what is new to the roots the output directory already has, after the blocks of the file of their type, without changing what they have; needed to import into an output directory again"`
	// Regenerate generates the roots Out already has again (--regenerate).
	Regenerate bool `json:"regenerate,omitempty" flag:"regenerate" jsonschema:"generate the roots the output directory already has again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed"`
}

// ImportResult is what an import did.
type ImportResult struct {
	ExitCode int                  `json:"exit_code"`
	Totals   report.CoverageTotal `json:"totals"`
	// FailedChecks are the verification checks that failed, by directory.
	FailedChecks []string `json:"failed_checks,omitempty"`
	// SkippedChecks are the verification checks that couldn't run, such
	// as the scanners check with no scanner installed.
	SkippedChecks []string `json:"skipped_checks,omitempty"`
	Failures      []string `json:"failures,omitempty"`
	Report        string   `json:"report"`
	// NotInCloud are the resources the roots have that an incremental
	// import didn't find in the cloud, where it listed everything of their
	// type: deleted since, for the user to remove from the roots or create
	// again.
	NotInCloud []report.NotInCloud `json:"not_in_cloud,omitempty"`
	// Moved are the resources that roots generated again move to other
	// addresses, and Departed those they hand over to other roots, which
	// must be applied after them.
	Moved    []report.Move `json:"moved,omitempty"`
	Departed []report.Move `json:"departed,omitempty"`
	// IncompleteHandovers are the handovers one of whose roots failed,
	// with what to do before applying either root.
	IncompleteHandovers []report.IncompleteHandover `json:"incomplete_handovers,omitempty"`
	// KeptRoots are the roots earlier imports generated that keep their
	// directories, though the configuration file's environments would put
	// their resources under envs/<env>/ now.
	KeptRoots []report.KeptRoot `json:"kept_roots,omitempty"`
	// Warnings are what people should look at before using the
	// configuration: about the run, such as environments it couldn't
	// apply, and about a directory's configuration, after its path.
	Warnings []string `json:"warnings,omitempty"`
}

// importSelection runs an import of a selection the user confirms: it
// checks the paths and the configuration, reads the selection once, asks
// the user to confirm it, and imports from a private copy of what they
// confirmed.
func (t *tools) importSelection(ctx context.Context, req *mcp.CallToolRequest, in ImportInput) (*mcp.CallToolResult, *ImportResult, error) {
	args, err := t.providers.commandArgs("import", in.Scope, in)
	if err != nil {
		return nil, nil, err
	}
	if err := t.importPaths(&in); err != nil {
		return nil, nil, err
	}
	// The check reads the private copy of it that the run reads.
	config, err := t.loadConfig(&in.Config, configUse{tool: "import", provider: in.Provider, providers: t.providers})
	if err != nil {
		return nil, nil, err
	}
	defer config.remove()
	scope, err := config.scope(in.Scope)
	if err != nil {
		return nil, nil, err
	}
	if err := checkQueryEngine(scope, config.engine(in.Engine)); err != nil {
		return nil, nil, err
	}
	args = append(args, config.flag()...)
	args = append(args, "--selection="+in.Selection, "--out="+in.Out, "--format=json")
	if err := t.check(ctx, config.run(args)); err != nil {
		return nil, nil, err
	}
	// The selection file is read once: the user confirms these bytes, and the
	// import reads them from a private copy, so a file the agent changes in
	// between, such as with a discover running at the same time, isn't what
	// is imported.
	_, content, err := t.readWithin("selection", in.Selection)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := newSnapshot("selection.yaml", content)
	if err != nil {
		return nil, nil, err
	}
	defer snapshot.remove()
	summary, err := summarize(snapshot.path)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", in.Selection, err)
	}

	// A person confirms the selection: an agent may not import on its own.
	// The tool asks the client to ask the user, and the client calls it
	// again with the answer (multi round-trip requests). The request state
	// ties the answer to this exact import.
	profile, err := t.providers.profile(in.Provider)
	if err != nil {
		return nil, nil, err
	}
	reads, err := scopeText(scope, profile)
	if err != nil {
		return nil, nil, err
	}
	if asked := t.confirm(req, userConfirmation{
		name:      confirmation,
		state:     confirmationState(args, content, config.sum()),
		message:   importMessage(in, summary, reads) + config.message(),
		command:   args,
		cannotAsk: "The user has to confirm an import",
		declined:  "The user didn't confirm the import, so nothing was imported.",
	}); asked != nil {
		return asked, nil, nil
	}

	childArgs := config.run(args)
	for i, a := range childArgs {
		if a == "--selection="+in.Selection {
			childArgs[i] = "--selection=" + snapshot.path
		}
	}
	stdout, stderr, code, err := t.runReporting(ctx, req, childArgs)
	if err != nil {
		return nil, nil, err
	}
	var r report.Report
	if err := json.Unmarshal(stdout, &r); err != nil {
		if stopped(code) {
			return errorResult(fmt.Sprintf("The import was stopped (exit code %d):\n%s", code, tail(stderr))), nil, nil
		}
		return errorResult(fmt.Sprintf("The import couldn't run (exit code %d):\n%s", code, tail(stderr))), nil, nil
	}
	result := importResult(in.Out, &r, code)
	return textResult(importText(in.Out, &r, result, code)), result, nil
}

// importPaths checks that an import has a selection and an output
// directory, both under the root, and sets them to the paths to use.
func (t *tools) importPaths(in *ImportInput) error {
	if in.Selection == "" || in.Out == "" {
		return errors.New("selection and out are required")
	}
	var err error
	if in.Selection, err = t.within("selection", in.Selection); err != nil {
		return err
	}
	if in.Out, err = t.within("out", in.Out); err != nil {
		return err
	}
	return t.linksStayUnder("out", in.Out)
}

// importMessage returns the question that asks the user to confirm an
// import, an incremental one or one that generates the roots again.
func importMessage(in ImportInput, summary *SelectionSummary, reads string) string {
	switch {
	case in.Regenerate:
		return fmt.Sprintf("Generate the roots in %q again, from scratch, from the %d resources that %q includes (%d excluded)? Their files are written again, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was. infraharvest reads %s and writes files; it never changes your cloud or your state.",
			in.Out, summary.Included, in.Selection, summary.Excluded, reads)
	case in.Incremental:
		return fmt.Sprintf("Add the resources that %q includes (%d excluded) and that the roots in %q don't have yet to those roots, after the blocks of the file of their type? What the roots have stays as it is. infraharvest reads %s and writes files; it never changes your cloud or your state.",
			in.Selection, summary.Excluded, in.Out, reads)
	}
	return fmt.Sprintf("Import the %d resources that %q includes (%d excluded) into Terraform configuration in %q? infraharvest reads %s and writes files; it never changes your cloud or your state.",
		summary.Included, in.Selection, summary.Excluded, in.Out, reads)
}

// importResult returns the tool's structured result of the import into
// out that wrote r and exited with code.
func importResult(out string, r *report.Report, code int) *ImportResult {
	return &ImportResult{
		ExitCode:      code,
		Totals:        r.Totals,
		FailedChecks:  r.FailedChecks(),
		SkippedChecks: r.SkippedChecks(),
		Failures:      r.Failures,
		Report:        filepath.Join(out, report.Dir, "report.md"),
		NotInCloud:    r.NotInCloud,
		Moved:         r.Moved,
		Departed:      r.Departed,

		IncompleteHandovers: r.IncompleteHandovers,
		KeptRoots:           r.KeptRoots,
		Warnings:            warnings(r),
	}
}

// importText returns the tool's text result: the import's totals, and
// what to look at before applying.
func importText(out string, r *report.Report, result *ImportResult, code int) string {
	text := fmt.Sprintf("Imported %d of %d resources into %s (exit code %d); %d left out, %d excluded, %d failed checks. The report is in %s.",
		r.Totals.Imported, r.Totals.Discovered, out, code, r.Totals.LeftOut, r.Totals.Excluded, len(result.FailedChecks), result.Report)
	if n := len(r.NotInCloud); n > 0 {
		text += fmt.Sprintf(" %d resources the roots have are no longer in the cloud (see not_in_cloud and the report): their next plan would create them again.", n)
	}
	if n := len(r.Departed); n > 0 {
		text += fmt.Sprintf(" %d resources go to other roots (see departed and the report): apply the root each was in before the root it goes to.", n)
	}
	if n := len(r.IncompleteHandovers); n > 0 {
		text += fmt.Sprintf(" %d handovers between roots aren't finished, as one of their roots failed (see incomplete_handovers and the report): do what each says before applying either root.", n)
	}
	if n := len(r.KeptRoots); n > 0 {
		text += fmt.Sprintf(" %d roots an earlier import generated keep their directories, though the configuration file's environments would put their resources under envs/<env>/ now (see kept_roots and the report): to lay them out by environment, import into a new output directory, or generate them again with --regenerate, which hands their resources over to the roots of their environments where removed blocks can take them all, the log saying why not.", n)
	}
	if n := len(result.Warnings); n > 0 {
		text += fmt.Sprintf(" %d warnings to look at before using the configuration (see warnings and the report).", n)
	}
	if stopped(code) {
		text = "The import was stopped before it finished. " + text
	}
	return text
}

// warnings returns the warnings of r: about the run, then about each
// directory's configuration, after the directory's path.
func warnings(r *report.Report) []string {
	w := slices.Clone(r.Warnings)
	for _, d := range r.Directories {
		for _, dw := range d.Warnings {
			w = append(w, d.Path+": "+dw)
		}
	}
	return w
}

// stopped reports whether a run that exited with code was stopped by a
// signal: one it caught, with the exit code infraharvest exits with on
// SIGINT or SIGTERM, or one that killed it, for which the exit code is
// negative.
func stopped(code int) bool {
	return code < 0 || code == report.ExitInterrupted || code == report.ExitTerminated
}

// confirmation names the import tool's request for the user's confirmation.
const confirmation = "confirm_import"

// confirmationState identifies a run the user is asked to confirm: its
// arguments and the whole content of the files it reads, such as its
// selection or decisions file and its configuration file, or a digest of
// them, as of the roots drift runs (see checkDriftRoots), so an agent
// can't change what runs once the user has confirmed, even keeping the
// counts the user was shown.
func confirmationState(args []string, contents ...[]byte) string {
	sum := sha256.New()
	for _, a := range args {
		fmt.Fprintf(sum, "%q\n", a)
	}
	// nil is a file the run doesn't read.
	for _, content := range contents {
		if content == nil {
			continue
		}
		fmt.Fprintf(sum, "file %x\n", sha256.Sum256(content))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// userConfirmation is a run the user is asked to confirm: an agent may
// not run it on its own.
type userConfirmation struct {
	// name names the request, and state identifies the run (see
	// confirmationState); message asks the user.
	name, state, message string
	// command is the run's arguments, which a client that can't ask is
	// given for the user to run themselves, after cannotAsk, which says
	// what the user has to confirm.
	command   []string
	cannotAsk string
	// declined is what the tool returns when the user didn't confirm.
	declined string
}

// confirm returns nil once the user has confirmed c: the client called
// the tool again with their answer to the request the server made for
// it (multi round-trip requests). Otherwise it returns what the tool
// returns: the request for the client to ask the user, which ties the
// answer to this exact run; the command to run, for a client that can't
// ask; or, for any answer but the user's acceptance of this request,
// that they didn't confirm.
func (t *tools) confirm(req *mcp.CallToolRequest, c userConfirmation) *mcp.CallToolResult {
	if len(req.Params.InputResponses) == 0 {
		if !canAsk(req.Session) {
			return errorResult(fmt.Sprintf("%s, and this client can't ask them. Ask the user to run it themselves:\n\ninfraharvest %s", c.cannotAsk, shellJoin(c.command)))
		}
		t.issue(c.state)
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{c.name: &mcp.ElicitParams{
				Message:         c.message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: c.state,
		}
	}
	answer, ok := req.Params.InputResponses[c.name].(*mcp.ElicitResult)
	// Only an answer to a request this server made counts, and only once.
	if !ok || answer.Action != "accept" || req.Params.RequestState != c.state || !t.redeem(c.state) {
		return errorResult(c.declined)
	}
	return nil
}

// maxIssued is how many runs the server waits for the user's answer on at
// once. A request the user never answers, or the client never calls back
// about, would otherwise be kept as long as the server runs; once there
// are more, the one asked longest ago is forgotten, and an answer to it
// is refused as one to a request never made.
const maxIssued = 64

// issue records that the server asked the user to confirm the run state
// identifies, forgetting the run asked about longest ago once more than
// maxIssued wait. A run asked about again counts from then.
func (t *tools) issue(state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.issued = slices.DeleteFunc(t.issued, func(s string) bool { return s == state })
	t.issued = append(t.issued, state)
	if len(t.issued) > maxIssued {
		t.issued = slices.Delete(t.issued, 0, len(t.issued)-maxIssued)
	}
}

// redeem reports whether the server asked the user to confirm the run
// state identifies, and forgets it: one confirmation runs one run.
func (t *tools) redeem(state string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := slices.Index(t.issued, state)
	if i < 0 {
		return false
	}
	t.issued = slices.Delete(t.issued, i, i+1)
	return true
}

// snapshot is a private copy of a file's content, such as a selection
// file's, at a path of its own named name.
type snapshot struct {
	dir, path string
}

func newSnapshot(name string, content []byte) (*snapshot, error) {
	dir, err := tfrun.MkdirTempHeld(tfrun.MCPPattern)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		tfrun.RemoveHeld(dir)
		return nil, err
	}
	return &snapshot{dir: dir, path: path}, nil
}

func (s *snapshot) remove() { tfrun.RemoveHeld(s.dir) }

// canAsk reports whether the client can ask its user to confirm.
func canAsk(session *mcp.ServerSession) bool {
	params := session.InitializeParams()
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

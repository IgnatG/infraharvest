// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package mcpserver serves infraharvest to AI agents over the Model Context
// Protocol. Its tools run the infraharvest binary itself, so they behave
// exactly like the command line: discover lists resources into a selection
// file, import generates Terraform configuration from one once a person
// confirms it, and report reads an import's report. Nothing is ever
// applied, and the cloud is only read.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// Runner runs infraharvest with args and returns its standard output, its
// standard error and its exit code. err is set only if it couldn't run.
type Runner func(ctx context.Context, args []string) (stdout, stderr []byte, exitCode int, err error)

// instructions tell agents how to use the tools.
const instructions = `infraharvest turns existing cloud resources into Terraform configuration that plans with no changes. It only reads the cloud and never applies anything.

1. Call discover to list a provider's resources into a selection file. Each resource is marked included or not, with the reason for exclusions.
2. Edit the selection file if the user wants a different selection, and show it to them.
3. Call import with the selection file. The user is asked to confirm before anything is imported. To import into an output directory that already has roots, such as after discover found new resources, set incremental: what is new is added to the roots without changing them. To generate those roots again from scratch, set regenerate instead.
4. Call report to read what was imported, what was left out and why, and the results of the checks.`

// New returns the server, which runs infraharvest with run. The tools read
// and write files under root only: selection files, output directories
// and reports.
func New(version, root string, run Runner) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "infraharvest", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	t := &tools{run: run, root: root}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "discover",
		Title:       "List resources into a selection file",
		Description: "Lists a provider's resources into a selection file, each marked included or excluded by the default rules (which leave out resources the cloud manages itself), and summarises it. Reads the cloud; writes only the selection file. " + scopeHelp,
		// It writes the selection file, merging into one that is there, so
		// it isn't read-only, though it destroys nothing.
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(true), DestructiveHint: ptr(false)},
	}, t.discover)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "import",
		Title:       "Generate Terraform configuration from a selection file",
		Description: "Imports the resources a selection file includes into Terraform configuration under the output directory, after the user confirms. Reads the cloud and writes files; never applies. Returns the import's totals and exit code. " + scopeHelp,
		// Not idempotent: the same import again is refused, with
		// incremental adds what is new since, or with regenerate writes
		// the roots again.
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(true), DestructiveHint: ptr(false)},
	}, t.importSelection)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "report",
		Title:       "Read an import's report",
		Description: "Returns the report of the import in an output directory: what was imported, left out and excluded, and the results of the verification checks.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, t.report)
	return server
}

func ptr[T any](v T) *T { return &v }

type tools struct {
	run Runner
	// root is the directory the tools' files must be under.
	root string

	mu sync.Mutex
	// issued are the states of the imports the server asked the user to
	// confirm, and hasn't run yet (see confirmationState).
	issued map[string]bool
}

// within checks that path, which the agent chose, is under the root: an
// agent may not read or write files anywhere else on the machine. The path
// has to be under it as written and where it leads, so a symbolic link
// under the root to somewhere else doesn't take it out. A relative path is
// relative to the root, not to the directory the server runs in; within
// returns the path to use, joined onto the root if it was relative.
func (t *tools) within(what, path string) (string, error) {
	if strings.ContainsFunc(path, unicode.IsControl) {
		// Such as a newline, which could make the confirmation the user is
		// shown say something else.
		return "", fmt.Errorf("%s %q has a control character", what, path)
	}
	root, err := filepath.Abs(t.root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return path, checkWithin(what, root, path)
}

// checkWithin checks that path, which is absolute, is under root (see
// within).
func checkWithin(what, root, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolvedRoot, err := resolve(root)
	if err != nil {
		return err
	}
	// Some systems take a ".." after a symbolic link up from where the
	// link leads, and others, Windows, up from the link: path must stay
	// under the root either way.
	for _, p := range []string{path, abs} {
		resolved, err := resolve(p)
		if err != nil {
			return err
		}
		if !under(resolvedRoot, resolved) {
			return fmt.Errorf("%s %q must be under %s", what, path, root)
		}
	}
	if !under(root, abs) {
		return fmt.Errorf("%s %q must be under %s", what, path, root)
	}
	return nil
}

// under reports whether path is dir or under it; both are absolute.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve returns where path leads, absolute: each symbolic link in it
// that exists is followed, and a ".." after one goes up from where the
// link leads. The part that doesn't exist yet is kept as it is.
func resolve(path string) (string, error) {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Not filepath.Join, which would take a ".." up from the element
		// before it rather than from where that leads.
		path = wd + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	parts := strings.FieldsFunc(path[len(volume):], func(r rune) bool { return r == '/' || r == filepath.Separator })
	for _, part := range parts {
		switch part {
		case ".":
		case "..":
			current = filepath.Dir(current)
		default:
			next := filepath.Join(current, part)
			resolved, err := filepath.EvalSymlinks(next)
			switch {
			case err == nil:
				current = resolved
			case errors.Is(err, fs.ErrNotExist):
				current = next
			default:
				return "", err
			}
		}
	}
	return current, nil
}

// Scope is what to list: a provider command, its services, and where to
// list them, in the provider's own terms. Each field sets the flag of the
// same name, and only the providers whose command has that flag take it.
type Scope struct {
	Provider string   `json:"provider" jsonschema:"the provider command: aws, azure or google"`
	Services []string `json:"services" jsonschema:"services to list, such as vpc, s3 and iam"`
	// Regions is for aws and google.
	Regions []string `json:"regions,omitempty" jsonschema:"aws and google only: regions to list, such as eu-west-2 for aws or global and europe-west1 for google (default: the profile's region for aws, global for google)"`
	// Profile and Accounts are for aws.
	Profile  string   `json:"profile,omitempty" jsonschema:"aws only: the AWS profile whose credentials to use"`
	Accounts []string `json:"accounts,omitempty" jsonschema:"aws only: accounts to list, each through the role infraharvest-readonly in it"`
	// Projects, Organization and Folders are for google, which needs at
	// least one of them.
	Projects     []string `json:"projects,omitempty" jsonschema:"google only: projects to list; google needs projects, organization or folders"`
	Organization string   `json:"organization,omitempty" jsonschema:"google only: an organization ID, to list every active project of it, in its folders too"`
	Folders      []string `json:"folders,omitempty" jsonschema:"google only: folder IDs, to list every active project of them, in their subfolders too"`
	// Subscriptions, ManagementGroup and ResourceGroup are for azure.
	Subscriptions   []string `json:"subscriptions,omitempty" jsonschema:"azure only: subscription IDs to list (default: ARM_SUBSCRIPTION_ID's)"`
	ManagementGroup string   `json:"management_group,omitempty" jsonschema:"azure only: a management group ID, to list every enabled subscription under it, in its child groups too"`
	ResourceGroup   string   `json:"resource_group,omitempty" jsonschema:"azure only: the resource group to list (default: the whole subscription)"`
}

// DiscoverInput is the discover tool's input.
type DiscoverInput struct {
	Scope
	Selection string `json:"selection" jsonschema:"the selection file to write, such as selection.yaml"`
}

// SelectionSummary counts a selection file's resources.
type SelectionSummary struct {
	Selection string      `json:"selection"`
	Included  int         `json:"included"`
	Excluded  int         `json:"excluded"`
	Types     []TypeCount `json:"types"`
	// Exclusions are the first excluded resources, up to maxExclusions,
	// with the reasons.
	Exclusions []string `json:"exclusions,omitempty"`
	// MoreExclusions counts the excluded resources Exclusions leaves out:
	// the selection file names every one.
	MoreExclusions int `json:"more_exclusions,omitempty"`
}

// maxExclusions is how many excluded resources a summary names: an
// organisation can have tens of thousands, too many for an agent's
// context, and the selection file has them all.
const maxExclusions = 50

// TypeCount counts one type's resources in a selection.
type TypeCount struct {
	Type     string `json:"type"`
	Included int    `json:"included"`
	Excluded int    `json:"excluded"`
}

func (t *tools) discover(ctx context.Context, _ *mcp.CallToolRequest, in DiscoverInput) (*mcp.CallToolResult, *SelectionSummary, error) {
	args, err := scopeArgs("discover", in.Scope)
	if err != nil {
		return nil, nil, err
	}
	if in.Selection == "" {
		return nil, nil, errors.New("selection is required")
	}
	if in.Selection, err = t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	// Never the picker: the client talks to this process over stdio.
	args = append(args, "--pick=false", "--selection="+in.Selection)
	_, stderr, code, err := t.run(ctx, args)
	if err != nil {
		return nil, nil, err
	}
	if stopped(code) {
		// It may have stopped before it wrote the selection file: one that
		// is there may be an earlier run's.
		return errorResult(fmt.Sprintf("discover was stopped (exit code %d), so the selection file is not read:\n%s", code, tail(stderr))), nil, nil
	}
	if code == report.ExitCouldNotRun {
		// Nothing was written: a selection file that is there is an old one.
		return errorResult(fmt.Sprintf("discover couldn't run (exit code %d):\n%s", code, tail(stderr))), nil, nil
	}
	summary, err := summarize(in.Selection)
	if err != nil {
		return nil, nil, fmt.Errorf("discover exited with %d: %w\n%s", code, err, tail(stderr))
	}
	text := fmt.Sprintf("%s lists %d resources: %d included, %d excluded.", in.Selection, summary.Included+summary.Excluded, summary.Included, summary.Excluded)
	if summary.MoreExclusions > 0 {
		text += fmt.Sprintf(" The summary names the first %d exclusions and leaves out %d more: %s has them all.", maxExclusions, summary.MoreExclusions, in.Selection)
	}
	if code != 0 {
		text += fmt.Sprintf(" Some services couldn't be listed (exit code %d):\n%s", code, tail(stderr))
	}
	return textResult(text), summary, nil
}

// summarize counts what a selection file includes.
func summarize(path string) (*SelectionSummary, error) {
	f, err := selection.Load(path)
	if err != nil {
		return nil, err
	}
	summary := &SelectionSummary{Selection: path}
	byType := map[string]*TypeCount{}
	for _, r := range f.Resources {
		c, ok := byType[r.Type]
		if !ok {
			c = &TypeCount{Type: r.Type}
			byType[r.Type] = c
		}
		d := f.DecideIn(r.Scope, r.Type, r.ID, r.Name, r.Tags)
		if d.Include {
			c.Included++
			summary.Included++
			continue
		}
		c.Excluded++
		summary.Excluded++
		if len(summary.Exclusions) == maxExclusions {
			summary.MoreExclusions++
			continue
		}
		summary.Exclusions = append(summary.Exclusions, fmt.Sprintf("%s %s: %s", r.Type, r.ID, d.Reason))
	}
	for _, c := range byType {
		summary.Types = append(summary.Types, *c)
	}
	sort.Slice(summary.Types, func(i, j int) bool { return summary.Types[i].Type < summary.Types[j].Type })
	return summary, nil
}

// ImportInput is the import tool's input.
type ImportInput struct {
	Scope
	Selection string `json:"selection" jsonschema:"the selection file to import, from discover"`
	Out       string `json:"out" jsonschema:"the directory to write the configuration and report to"`
	Engine    string `json:"engine,omitempty" jsonschema:"terraform (the default) or tofu"`
	// Incremental adds to the roots Out already has (--incremental).
	Incremental bool `json:"incremental,omitempty" jsonschema:"add what is new to the roots the output directory already has, in files of their own, without changing what they have; needed to import into an output directory again"`
	// Regenerate generates the roots Out already has again (--regenerate).
	Regenerate bool `json:"regenerate,omitempty" jsonschema:"generate the roots the output directory already has again, from scratch, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was and reported as failed"`
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
}

func (t *tools) importSelection(ctx context.Context, req *mcp.CallToolRequest, in ImportInput) (*mcp.CallToolResult, *ImportResult, error) {
	args, err := scopeArgs("import", in.Scope)
	if err != nil {
		return nil, nil, err
	}
	if in.Selection == "" || in.Out == "" {
		return nil, nil, errors.New("selection and out are required")
	}
	if in.Incremental && in.Regenerate {
		return nil, nil, errors.New("set incremental or regenerate, not both")
	}
	if in.Selection, err = t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	if in.Out, err = t.within("out", in.Out); err != nil {
		return nil, nil, err
	}
	if err := t.linksStayUnder("out", in.Out); err != nil {
		return nil, nil, err
	}
	engine := in.Engine
	if engine == "" {
		engine = "terraform"
	}
	if engine != "terraform" && engine != "tofu" {
		return nil, nil, fmt.Errorf("engine must be terraform or tofu, not %q", engine)
	}
	args = append(args, "--engine="+engine, "--selection="+in.Selection, "--out="+in.Out, "--format=json")
	if in.Incremental {
		args = append(args, "--incremental")
	}
	if in.Regenerate {
		args = append(args, "--regenerate")
	}
	// The selection file is read once: the user confirms these bytes, and the
	// import reads them from a private copy, so a file the agent changes in
	// between, such as with a discover running at the same time, isn't what
	// is imported.
	content, err := os.ReadFile(in.Selection)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := newSnapshot(content)
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
	message := fmt.Sprintf("Import the %d resources that %q includes (%d excluded) into Terraform configuration in %q? infraharvest reads %s and writes files; it never applies anything.",
		summary.Included, in.Selection, summary.Excluded, in.Out, scopeText(in.Scope))
	if in.Incremental {
		message = fmt.Sprintf("Add the resources that %q includes (%d excluded) and that the roots in %q don't have yet to those roots, in files of their own? What the roots have stays as it is. infraharvest reads %s and writes files; it never applies anything.",
			in.Selection, summary.Excluded, in.Out, scopeText(in.Scope))
	}
	if in.Regenerate {
		message = fmt.Sprintf("Generate the roots in %q again, from scratch, from the %d resources that %q includes (%d excluded)? Their files are written again, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was. infraharvest reads %s and writes files; it never applies anything.",
			in.Out, summary.Included, in.Selection, summary.Excluded, scopeText(in.Scope))
	}
	state := confirmationState(args, content)
	if len(req.Params.InputResponses) == 0 {
		if !canAsk(req.Session) {
			return errorResult(fmt.Sprintf("The user has to confirm an import, and this client can't ask them. Ask the user to run it themselves:\n\ninfraharvest %s", shellJoin(args))), nil, nil
		}
		t.issue(state)
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{confirmation: &mcp.ElicitParams{
				Message:         message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: state,
		}, nil, nil
	}
	answer, ok := req.Params.InputResponses[confirmation].(*mcp.ElicitResult)
	// Only an answer to a request this server made counts, and only once.
	if !ok || answer.Action != "accept" || req.Params.RequestState != state || !t.redeem(state) {
		return errorResult("The user didn't confirm the import, so nothing was imported."), nil, nil
	}

	childArgs := slices.Clone(args)
	for i, a := range childArgs {
		if a == "--selection="+in.Selection {
			childArgs[i] = "--selection=" + snapshot.path
		}
	}
	stdout, stderr, code, err := t.run(ctx, childArgs)
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
	result := &ImportResult{
		ExitCode:      code,
		Totals:        r.Totals,
		FailedChecks:  r.FailedChecks(),
		SkippedChecks: r.SkippedChecks(),
		Failures:      r.Failures,
		Report:        filepath.Join(in.Out, report.Dir, "report.md"),
		NotInCloud:    r.NotInCloud,
		Moved:         r.Moved,
		Departed:      r.Departed,
	}
	text := fmt.Sprintf("Imported %d of %d resources into %s (exit code %d); %d left out, %d excluded, %d failed checks. The report is in %s.",
		r.Totals.Imported, r.Totals.Discovered, in.Out, code, r.Totals.LeftOut, r.Totals.Excluded, len(result.FailedChecks), result.Report)
	if n := len(r.NotInCloud); n > 0 {
		text += fmt.Sprintf(" %d resources the roots have are no longer in the cloud (see not_in_cloud and the report): their next plan would create them again.", n)
	}
	if n := len(r.Departed); n > 0 {
		text += fmt.Sprintf(" %d resources go to other roots (see departed and the report): apply the root each was in before the root it goes to.", n)
	}
	if stopped(code) {
		text = "The import was stopped before it finished. " + text
	}
	return textResult(text), result, nil
}

// Exit codes of a run a signal stopped, 128 and the signal's number, as
// infraharvest exits on SIGINT and SIGTERM.
const (
	exitInterrupted = 130
	exitTerminated  = 143
)

// stopped reports whether a run that exited with code was stopped by a
// signal: one it caught, or one that killed it, for which the exit code is
// negative.
func stopped(code int) bool {
	return code < 0 || code == exitInterrupted || code == exitTerminated
}

// confirmation names the import tool's request for the user's confirmation.
const confirmation = "confirm_import"

// confirmationState identifies an import the user is asked to confirm:
// its arguments and the whole content of its selection file, so an agent
// can't change what is imported once the user has confirmed, even keeping
// the counts the user was shown.
func confirmationState(args []string, content []byte) string {
	sum := sha256.New()
	for _, a := range args {
		fmt.Fprintf(sum, "%q\n", a)
	}
	selectionSum := sha256.Sum256(content)
	fmt.Fprintf(sum, "selection %x\n", selectionSum)
	return hex.EncodeToString(sum.Sum(nil))
}

// issue records that the server asked the user to confirm the import
// state identifies.
func (t *tools) issue(state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.issued == nil {
		t.issued = map[string]bool{}
	}
	t.issued[state] = true
}

// redeem reports whether the server asked the user to confirm the import
// state identifies, and forgets it: one confirmation runs one import.
func (t *tools) redeem(state string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.issued[state] {
		return false
	}
	delete(t.issued, state)
	return true
}

// snapshot is a private copy of a selection file's content.
type snapshot struct {
	dir, path string
}

func newSnapshot(content []byte) (*snapshot, error) {
	dir, err := os.MkdirTemp("", "infraharvest-mcp-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "selection.yaml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &snapshot{dir: dir, path: path}, nil
}

func (s *snapshot) remove() { _ = os.RemoveAll(s.dir) }

// canAsk reports whether the client can ask its user to confirm.
func canAsk(session *mcp.ServerSession) bool {
	params := session.InitializeParams()
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

// ReportInput is the report tool's input.
type ReportInput struct {
	Out string `json:"out" jsonschema:"the output directory of an import"`
}

func (t *tools) report(_ context.Context, _ *mcp.CallToolRequest, in ReportInput) (*mcp.CallToolResult, any, error) {
	if in.Out == "" {
		return nil, nil, errors.New("out is required")
	}
	out, err := t.within("out", in.Out)
	if err != nil {
		return nil, nil, err
	}
	in.Out = out
	// The report's own path too: .infraharvest or report.md could be a
	// symbolic link that leads out of the root, such as one a cloned
	// repository plants to a credentials file.
	path := filepath.Join(in.Out, report.Dir, "report.md")
	if _, err := t.within("report", path); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("report %q isn't a regular file", path)
	}
	content, size, err := readHead(path, maxReport)
	if err != nil {
		return nil, nil, fmt.Errorf("no report in %s: %w", in.Out, err)
	}
	text := string(content)
	if left := size - int64(len(content)); left > 0 {
		text += fmt.Sprintf("\n\n[%d of the report's %d bytes are left out here: the full report is %s]", left, size, path)
	}
	return textResult(text), nil, nil
}

// maxReport is the most bytes of a report the report tool returns: a
// report is mostly counts by type, but one of a large import could still
// fill an agent's context.
const maxReport = 256 << 10

// readHead reads at most limit bytes of the file at path, cut after the
// last whole line that fits, or else the last whole character, and
// returns them with the file's size.
func readHead(path string, limit int) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	content, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, 0, err
	}
	size := max(info.Size(), int64(len(content)))
	if len(content) <= limit {
		return content, size, nil
	}
	return content[:cutAt(content, limit)], size, nil
}

// cutAt returns where to cut b, which is longer than limit, so that what
// is kept fits in limit and ends with a whole line, or else a whole
// character.
func cutAt(b []byte, limit int) int {
	cut := limit
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	if i := bytes.LastIndexByte(b[:cut], '\n'); i >= 0 {
		cut = i + 1
	}
	return cut
}

// linksStayUnder checks that no symbolic link under dir, which an import
// writes into, leads out of the root, as one a cloned repository plants
// could: the import would write there. Terraform's working directories
// are skipped, where terraform init links providers from the plugin cache.
func (t *tools) linksStayUnder(what, dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			_, err := t.within(what, path)
			return err
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// providers are the provider commands the tools run.
var providers = []string{"aws", "azure", "google"}

// scopeField is one of Scope's fields that say where to list: the name an
// agent sends it by, the flag it sets, the providers whose command has
// that flag, and its values (none when it isn't set).
type scopeField struct {
	name      string
	flag      string
	providers []string
	values    func(Scope) []string
}

// scopeFields are Scope's fields that say where to list, in the order
// their flags are passed.
var scopeFields = []scopeField{
	{"regions", "regions", []string{"aws", "google"}, func(s Scope) []string { return s.Regions }},
	{"profile", "profile", []string{"aws"}, func(s Scope) []string { return nonEmpty(s.Profile) }},
	{"accounts", "accounts", []string{"aws"}, func(s Scope) []string { return s.Accounts }},
	{"projects", "projects", []string{"google"}, func(s Scope) []string { return s.Projects }},
	{"organization", "organization", []string{"google"}, func(s Scope) []string { return nonEmpty(s.Organization) }},
	{"folders", "folders", []string{"google"}, func(s Scope) []string { return s.Folders }},
	{"subscriptions", "subscriptions", []string{"azure"}, func(s Scope) []string { return s.Subscriptions }},
	{"management_group", "management-group", []string{"azure"}, func(s Scope) []string { return nonEmpty(s.ManagementGroup) }},
	{"resource_group", "resource-group", []string{"azure"}, func(s Scope) []string { return nonEmpty(s.ResourceGroup) }},
}

// nonEmpty returns value as the only value of a field, or none if it is
// empty.
func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// scopeHelp says which provider takes which of Scope's fields, for the
// tools' descriptions.
const scopeHelp = "Where to list depends on the provider: aws takes regions, profile and accounts; azure takes subscriptions, management_group and resource_group; google takes regions and needs projects, organization or folders."

// scopeArgs returns the arguments for command on scope. Every value is
// part of a --flag=value argument, so none can become a flag of its own.
// A field the provider's command has no flag for is rejected rather than
// left for the command to fail on, and so is a Google scope without a
// project, organization or folder.
func scopeArgs(command string, s Scope) ([]string, error) {
	if !slices.Contains(providers, s.Provider) {
		return nil, fmt.Errorf("unknown provider %q: use %s", s.Provider, strings.Join(providers, ", "))
	}
	if len(s.Services) == 0 {
		return nil, errors.New("services is required")
	}
	if err := checkScopeValues(s); err != nil {
		return nil, err
	}
	args := []string{command, s.Provider, "--services=" + strings.Join(s.Services, ",")}
	var errs []error
	for _, f := range scopeFields {
		values := f.values(s)
		if len(values) == 0 {
			continue
		}
		if !slices.Contains(f.providers, s.Provider) {
			errs = append(errs, fmt.Errorf("%s doesn't take %s, which is for %s only", s.Provider, f.name, strings.Join(f.providers, " and ")))
			continue
		}
		args = append(args, "--"+f.flag+"="+strings.Join(values, ","))
	}
	if s.Provider == "google" && len(s.Projects) == 0 && s.Organization == "" && len(s.Folders) == 0 {
		errs = append(errs, errors.New("google needs projects, organization or folders to say which projects to list"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return args, nil
}

// namePattern is what a service or a region is named like: vpc, s3,
// resource_graph, eu-west-2, global, or * for all.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_*.-]+$`)

// checkScopeValues rejects a service or region that isn't named like one,
// and any value with a control character, such as a newline, which could
// make the confirmation the user is shown say something else.
func checkScopeValues(s Scope) error {
	for _, v := range append(slices.Clone(s.Services), s.Regions...) {
		if !namePattern.MatchString(v) {
			return fmt.Errorf("%q isn't a service or a region", v)
		}
	}
	values := []string{s.Profile, s.Organization, s.ManagementGroup, s.ResourceGroup}
	for _, list := range [][]string{s.Accounts, s.Projects, s.Folders, s.Subscriptions} {
		values = append(values, list...)
	}
	for _, v := range values {
		if strings.ContainsFunc(v, unicode.IsControl) {
			return fmt.Errorf("%q has a control character", v)
		}
	}
	return nil
}

// shellJoin joins args into a command line a POSIX shell reads back as
// args: each argument with anything but letters, digits and -_.,:/=@+ in
// it is single-quoted, so a path with a space or a ; stays one argument.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_.,:/=@+-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// scopeText says what an import reads, for the user to confirm: the
// provider's accounts, subscriptions or projects, where in them, and the
// services. scope has passed scopeArgs.
func scopeText(s Scope) string {
	var text string
	switch s.Provider {
	case "azure":
		var parts []string
		if len(s.Subscriptions) > 0 {
			parts = append(parts, "subscriptions "+strings.Join(s.Subscriptions, ", "))
		}
		if s.ManagementGroup != "" {
			parts = append(parts, "every enabled subscription under management group "+s.ManagementGroup)
		}
		if len(parts) == 0 {
			parts = append(parts, "subscription of ARM_SUBSCRIPTION_ID")
		}
		text = "the azure " + strings.Join(parts, " and ")
		if s.ResourceGroup != "" {
			text = "resource group " + s.ResourceGroup + " of " + text
		}
	case "google":
		var parts, owners []string
		if len(s.Projects) > 0 {
			parts = append(parts, "the google projects "+strings.Join(s.Projects, ", "))
		}
		if s.Organization != "" {
			owners = append(owners, "organization "+s.Organization)
		}
		if len(s.Folders) > 0 {
			owners = append(owners, "folders "+strings.Join(s.Folders, ", "))
		}
		if len(owners) > 0 {
			parts = append(parts, "every active google project of "+strings.Join(owners, " and of "))
		}
		regions := s.Regions
		if len(regions) == 0 {
			// The google command's default.
			regions = []string{"global"}
		}
		text = strings.Join(parts, " and ") + " in " + strings.Join(regions, ", ")
	default:
		text = "the aws account"
		if len(s.Accounts) > 0 {
			text = "the aws accounts " + strings.Join(s.Accounts, ", ")
		}
		if s.Profile != "" {
			text += " with profile " + s.Profile
		}
		if len(s.Regions) > 0 {
			text += " in " + strings.Join(s.Regions, ", ")
		}
	}
	return text + " (" + strings.Join(s.Services, ", ") + ")"
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(text string) *mcp.CallToolResult {
	r := textResult(text)
	r.IsError = true
	return r
}

// tail returns the last lines of a command's standard error, each cut to
// maxLine bytes.
func tail(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(bytes.TrimSpace(stderr))), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	for i, line := range lines {
		if len(line) > maxLine {
			lines[i] = line[:cutAt([]byte(line), maxLine)] + " [cut]"
		}
	}
	return strings.Join(lines, "\n")
}

// maxLine is the most bytes of one line of standard error a result
// quotes.
const maxLine = 2000

// CheckDefaultRoot rejects dir as the root a server defaults to, the
// working directory, when it is the root of a filesystem or home, the
// user's home directory: some hosts start servers there, and the tools
// could then read and write files anywhere on the machine, or anywhere in
// the home directory. Naming it with --root still allows it.
func CheckDefaultRoot(dir, home string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if filepath.Dir(abs) == abs {
		return fmt.Errorf("the working directory %s is the root of the filesystem: start the server in a project directory, or name the directory the tools may use with --root", abs)
	}
	if home != "" {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		if h, err := filepath.Abs(home); err == nil && sameDir(abs, h) {
			return fmt.Errorf("the working directory %s is the home directory: start the server in a project directory, or name the directory the tools may use with --root", abs)
		}
	}
	return nil
}

// sameDir reports whether a and b, absolute and clean, are the same
// directory: Windows paths are the same in any case.
func sameDir(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

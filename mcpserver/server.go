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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
)

// Runner runs infraharvest with args and returns its standard output, its
// standard error and its exit code. err is set only if it couldn't run.
type Runner func(ctx context.Context, args []string) (stdout, stderr []byte, exitCode int, err error)

// instructions tell agents how to use the tools.
const instructions = `infraharvest turns existing cloud resources into Terraform configuration that plans with no changes. It only reads the cloud and never applies anything.

1. Call discover to list a provider's resources into a selection file. Each resource is marked included or not, with the reason for exclusions.
2. Edit the selection file if the user wants a different selection, and show it to them.
3. Call import with the selection file. The user is asked to confirm before anything is imported. To import into an output directory that already has roots, such as after discover found new resources, set incremental: what is new is added to the roots without changing them.
4. Call report to read what was imported, what was left out and why, and the results of the checks.`

// New returns the server, which runs infraharvest with run. The tools read
// and write files under root only: selection files, output directories
// and reports.
func New(version, root string, run Runner) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "infraharvest", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	t := &tools{run: run, root: root}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "discover",
		Title:       "List resources into a selection file",
		Description: "Lists a provider's resources into a selection file, each marked included or excluded by the default rules (which leave out resources the cloud manages itself), and summarises it. Reads the cloud; writes only the selection file.",
		Annotations: readOnly,
	}, t.discover)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "import",
		Title:       "Generate Terraform configuration from a selection file",
		Description: "Imports the resources a selection file includes into Terraform configuration under the output directory, after the user confirms. Reads the cloud and writes files; never applies. Returns the import's totals and exit code.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(true), DestructiveHint: ptr(false), IdempotentHint: true},
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
}

// within checks that path, which the agent chose, is under the root: an
// agent may not read or write files anywhere else on the machine.
func (t *tools) within(what, path string) error {
	root, err := filepath.Abs(t.root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q must be under %s", what, path, root)
	}
	return nil
}

// Scope is what to list: a provider command and its resources and regions.
type Scope struct {
	Provider  string   `json:"provider" jsonschema:"the provider command, such as aws"`
	Resources []string `json:"resources" jsonschema:"services to list, such as vpc, s3 and iam"`
	Regions   []string `json:"regions,omitempty" jsonschema:"regions to list, such as eu-west-2"`
	Profile   string   `json:"profile,omitempty" jsonschema:"the provider profile to use, such as an AWS profile"`
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
	// Exclusions are the excluded resources with the reasons.
	Exclusions []string `json:"exclusions,omitempty"`
}

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
	if err := t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	// Never the picker: the client talks to this process over stdio.
	args = append(args, "--pick=false", "--selection="+in.Selection)
	_, stderr, code, err := t.run(ctx, args)
	if err != nil {
		return nil, nil, err
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
	Output    string `json:"output" jsonschema:"the directory to write the configuration and report to"`
	Engine    string `json:"engine,omitempty" jsonschema:"terraform (the default) or tofu"`
	// Incremental adds to the roots Output already has (--incremental).
	Incremental bool `json:"incremental,omitempty" jsonschema:"add what is new to the roots the output directory already has, in files of their own, without changing what they have; needed to import into an output directory again"`
}

// ImportResult is what an import did.
type ImportResult struct {
	ExitCode int                  `json:"exit_code"`
	Totals   report.CoverageTotal `json:"totals"`
	// FailedChecks are the verification checks that failed, by directory.
	FailedChecks []string `json:"failed_checks,omitempty"`
	Failures     []string `json:"failures,omitempty"`
	Report       string   `json:"report"`
}

func (t *tools) importSelection(ctx context.Context, req *mcp.CallToolRequest, in ImportInput) (*mcp.CallToolResult, *ImportResult, error) {
	args, err := scopeArgs("import", in.Scope)
	if err != nil {
		return nil, nil, err
	}
	if in.Selection == "" || in.Output == "" {
		return nil, nil, errors.New("selection and output are required")
	}
	if err := t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	if err := t.within("output", in.Output); err != nil {
		return nil, nil, err
	}
	engine := in.Engine
	if engine == "" {
		engine = "terraform"
	}
	if engine != "terraform" && engine != "tofu" {
		return nil, nil, fmt.Errorf("engine must be terraform or tofu, not %q", engine)
	}
	args = append(args, "--engine="+engine, "--selection="+in.Selection, "--path-output="+in.Output, "--output=json")
	if in.Incremental {
		args = append(args, "--incremental")
	}
	summary, err := summarize(in.Selection)
	if err != nil {
		return nil, nil, err
	}

	// A person confirms the selection: an agent may not import on its own.
	// The tool asks the client to ask the user, and the client calls it
	// again with the answer (multi round-trip requests). The request state
	// ties the answer to this exact import.
	message := fmt.Sprintf("Import the %d resources that %s includes (%d excluded) into Terraform configuration in %s? infraharvest reads %s and writes files; it never applies anything.",
		summary.Included, in.Selection, summary.Excluded, in.Output, scopeText(in.Scope))
	if in.Incremental {
		message = fmt.Sprintf("Add the resources that %s includes (%d excluded) and that the roots in %s don't have yet to those roots, in files of their own? What the roots have stays as it is. infraharvest reads %s and writes files; it never applies anything.",
			in.Selection, summary.Excluded, in.Output, scopeText(in.Scope))
	}
	state := confirmationState(args, summary)
	if len(req.Params.InputResponses) == 0 {
		if !canAsk(req.Session) {
			return errorResult(fmt.Sprintf("The user has to confirm an import, and this client can't ask them. Ask the user to run it themselves:\n\ninfraharvest %s", strings.Join(args, " "))), nil, nil
		}
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{confirmation: &mcp.ElicitParams{
				Message:         message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: state,
		}, nil, nil
	}
	answer, ok := req.Params.InputResponses[confirmation].(*mcp.ElicitResult)
	if !ok || answer.Action != "accept" || req.Params.RequestState != state {
		return errorResult("The user didn't confirm the import, so nothing was imported."), nil, nil
	}

	stdout, stderr, code, err := t.run(ctx, args)
	if err != nil {
		return nil, nil, err
	}
	var r report.Report
	if err := json.Unmarshal(stdout, &r); err != nil {
		return errorResult(fmt.Sprintf("The import couldn't run (exit code %d):\n%s", code, tail(stderr))), nil, nil
	}
	result := &ImportResult{
		ExitCode:     code,
		Totals:       r.Totals,
		FailedChecks: r.FailedChecks(),
		Failures:     r.Failures,
		Report:       filepath.Join(in.Output, report.Dir, "report.md"),
	}
	text := fmt.Sprintf("Imported %d of %d resources into %s (exit code %d); %d left out, %d excluded, %d failed checks. The report is in %s.",
		r.Totals.Imported, r.Totals.Discovered, in.Output, code, r.Totals.LeftOut, r.Totals.Excluded, len(result.FailedChecks), result.Report)
	return textResult(text), result, nil
}

// confirmation names the import tool's request for the user's confirmation.
const confirmation = "confirm_import"

// confirmationState identifies an import the user is asked to confirm:
// its arguments and what its selection file includes.
func confirmationState(args []string, summary *SelectionSummary) string {
	sum := sha256.New()
	for _, a := range args {
		fmt.Fprintf(sum, "%q\n", a)
	}
	fmt.Fprintf(sum, "%d %d\n", summary.Included, summary.Excluded)
	return hex.EncodeToString(sum.Sum(nil))
}

// canAsk reports whether the client can ask its user to confirm.
func canAsk(session *mcp.ServerSession) bool {
	params := session.InitializeParams()
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

// ReportInput is the report tool's input.
type ReportInput struct {
	Output string `json:"output" jsonschema:"the output directory of an import"`
}

func (t *tools) report(_ context.Context, _ *mcp.CallToolRequest, in ReportInput) (*mcp.CallToolResult, any, error) {
	if in.Output == "" {
		return nil, nil, errors.New("output is required")
	}
	if err := t.within("output", in.Output); err != nil {
		return nil, nil, err
	}
	content, err := os.ReadFile(filepath.Join(in.Output, report.Dir, "report.md"))
	if err != nil {
		return nil, nil, fmt.Errorf("no report in %s: %w", in.Output, err)
	}
	return textResult(string(content)), nil, nil
}

// providerName is what a provider command name looks like.
var providerName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// scopeArgs returns the arguments for command on scope. Every value is
// part of a --flag=value argument, so none can become a flag of its own.
func scopeArgs(command string, s Scope) ([]string, error) {
	if !providerName.MatchString(s.Provider) {
		return nil, fmt.Errorf("unknown provider %q", s.Provider)
	}
	if len(s.Resources) == 0 {
		return nil, errors.New("resources is required")
	}
	args := []string{command, s.Provider, "--resources=" + strings.Join(s.Resources, ",")}
	if len(s.Regions) > 0 {
		args = append(args, "--regions="+strings.Join(s.Regions, ","))
	}
	if s.Profile != "" {
		args = append(args, "--profile="+s.Profile)
	}
	return args, nil
}

// scopeText says what an import reads, for the user to confirm: the
// provider's account (its profile), regions and services.
func scopeText(s Scope) string {
	text := "the " + s.Provider + " account"
	if s.Profile != "" {
		text += " of profile " + s.Profile
	}
	if len(s.Regions) > 0 {
		text += " in " + strings.Join(s.Regions, ", ")
	}
	return text + " (" + strings.Join(s.Resources, ", ") + ")"
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(text string) *mcp.CallToolResult {
	r := textResult(text)
	r.IsError = true
	return r
}

// tail returns the last lines of a command's standard error.
func tail(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(bytes.TrimSpace(stderr))), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

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
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

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
		Description: "Lists a provider's resources into a selection file, each marked included or excluded by the default rules (which leave out resources the cloud manages itself), and summarises it. Reads the cloud; writes only the selection file. " + scopeHelp,
		Annotations: readOnly,
	}, t.discover)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "import",
		Title:       "Generate Terraform configuration from a selection file",
		Description: "Imports the resources a selection file includes into Terraform configuration under the output directory, after the user confirms. Reads the cloud and writes files; never applies. Returns the import's totals and exit code. " + scopeHelp,
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
// agent may not read or write files anywhere else on the machine. The path
// has to be under it as written and where it leads, so a symbolic link
// under the root to somewhere else doesn't take it out.
func (t *tools) within(what, path string) error {
	root, err := filepath.Abs(t.root)
	if err != nil {
		return err
	}
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
	Out       string `json:"out" jsonschema:"the directory to write the configuration and report to"`
	Engine    string `json:"engine,omitempty" jsonschema:"terraform (the default) or tofu"`
	// Incremental adds to the roots Out already has (--incremental).
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
	if in.Selection == "" || in.Out == "" {
		return nil, nil, errors.New("selection and out are required")
	}
	if err := t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	if err := t.within("out", in.Out); err != nil {
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
	content, err := os.ReadFile(in.Selection)
	if err != nil {
		return nil, nil, err
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
		summary.Included, in.Selection, summary.Excluded, in.Out, scopeText(in.Scope))
	if in.Incremental {
		message = fmt.Sprintf("Add the resources that %s includes (%d excluded) and that the roots in %s don't have yet to those roots, in files of their own? What the roots have stays as it is. infraharvest reads %s and writes files; it never applies anything.",
			in.Selection, summary.Excluded, in.Out, scopeText(in.Scope))
	}
	state := confirmationState(args, content)
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
		Report:       filepath.Join(in.Out, report.Dir, "report.md"),
	}
	text := fmt.Sprintf("Imported %d of %d resources into %s (exit code %d); %d left out, %d excluded, %d failed checks. The report is in %s.",
		r.Totals.Imported, r.Totals.Discovered, in.Out, code, r.Totals.LeftOut, r.Totals.Excluded, len(result.FailedChecks), result.Report)
	return textResult(text), result, nil
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
	if err := t.within("out", in.Out); err != nil {
		return nil, nil, err
	}
	content, err := os.ReadFile(filepath.Join(in.Out, report.Dir, "report.md"))
	if err != nil {
		return nil, nil, fmt.Errorf("no report in %s: %w", in.Out, err)
	}
	return textResult(string(content)), nil, nil
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

// tail returns the last lines of a command's standard error.
func tail(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(bytes.TrimSpace(stderr))), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

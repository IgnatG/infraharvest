// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package mcpserver serves infraharvest to AI agents over the Model Context
// Protocol. Its tools run the infraharvest binary itself, so they behave
// exactly like the command line: discover lists resources into a selection
// file, import generates Terraform configuration from one once a person
// confirms it, report reads an import's report or the drift report, drift
// reports what changed in the cloud since the roots were applied, and
// drift_accept takes the cloud's values into the code once a person
// confirms it. Each provider's parameters are declared on the tools'
// inputs, with the flag each sets, and a parameter for another provider
// is rejected. An import is checked as the command line's --check checks
// it before the person is asked, so a run the command would refuse is
// never put to them. Nothing is ever applied, the state is never changed,
// and the cloud is only read.
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
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// Runner runs infraharvest with args and returns its standard output, its
// standard error and its exit code. err is set only if it couldn't run.
type Runner func(ctx context.Context, args []string) (stdout, stderr []byte, exitCode int, err error)

// Checker checks infraharvest's args as the command they name would before
// running, without running it, and returns the error the command would
// fail with.
type Checker func(ctx context.Context, args []string) error

// instructions tell agents how to use the tools.
const instructions = `infraharvest turns existing cloud resources into Terraform configuration that plans with no changes. It only reads the cloud and never applies anything.

1. Call discover to list a provider's resources into a selection file. Each resource is marked included or not, with the reason for exclusions.
2. Edit the selection file if the user wants a different selection, and show it to them.
3. Call import with the selection file. The user is asked to confirm before anything is imported. To import into an output directory that already has roots, such as after discover found new resources, set incremental: what is new is added to the roots without changing them. To generate those roots again from scratch, set regenerate instead.
4. Call report to read what was imported, what was left out and why, and the results of the checks.
5. Once the user has applied the roots, call drift to see what changed in the cloud since, and what the next apply would do about each change: drift changes neither the code nor the state. Call report with kind drift to read the drift report it wrote. To take the cloud's values into the code instead of letting the next apply revert them, call drift_accept with rules or a decisions file; the user is asked to confirm first, seeing how many changes would be accepted. With write_decisions, drift_accept writes a decisions file with an entry per change, for the user to decide each, then apply with decisions.`

// New returns the server, which runs infraharvest with run, and checks an
// import with check before the user is asked to confirm it. The tools read
// and write files under root only: selection files, output directories,
// reports and drift decisions files.
func New(version, root string, run Runner, check Checker) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "infraharvest", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	t := &tools{run: run, check: check, root: root}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "discover",
		Title:       "List resources into a selection file",
		Description: "Lists a provider's resources into a selection file, each marked included or excluded by the default rules (which leave out resources the cloud manages itself), and summarises it. Reads the cloud; writes only the selection file. " + scopeHelp,
		InputSchema: scopeSchema[DiscoverInput](),
		// It writes the selection file, merging into one that is there, so
		// it isn't read-only, though it destroys nothing.
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(true), DestructiveHint: ptr(false)},
	}, t.discover)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "import",
		Title:       "Generate Terraform configuration from a selection file",
		Description: "Imports the resources a selection file includes into Terraform configuration under the output directory, after the user confirms. Reads the cloud and writes files; never applies. Returns the import's totals and exit code. " + scopeHelp,
		InputSchema: scopeSchema[ImportInput](),
		// Not idempotent: the same import again is refused, with
		// incremental adds what is new since, or with regenerate writes
		// the roots again.
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(true), DestructiveHint: ptr(false)},
	}, t.importSelection)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "report",
		Title:       "Read an import's report or the drift report",
		Description: "Returns the report of the import in an output directory: what was imported, left out and excluded, and the results of the verification checks; or, with kind drift, the drift report: the changes in the cloud since the roots were applied, and what became of each.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, t.report)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "drift",
		Title:       "Find what changed in the cloud since the roots were applied",
		Description: "Checks the roots an import generated in the output directory for what changed in the cloud since they were applied, and what the next apply of their code would do about each change: revert it, or keep it as the code doesn't set it. Reads the cloud and the roots' state with the environment's credentials; changes neither the code nor the state, and writes only the drift report and Terraform's working files. " + driftExitHelp,
		// It writes the drift report, and the roots' .terraform directories
		// as an init does, but changes nothing a person keeps.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)},
	}, t.drift)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "drift_accept",
		Title:       "Take changes in the cloud into the code",
		Description: "Checks the roots as drift does, and writes the cloud's values of the changes that its rules or a decisions file accept into the roots' code, after the user confirms; each edit is kept only once the plan of the edited code shows the resource taking it and nothing else changing. Secrets are never written. With write_decisions, writes the run's changes into a decisions file, which needs no confirmation on its own as it changes no code. Never applies, and never changes the state. " + driftExitHelp,
		// Not idempotent: an accepted change is no longer drift.
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(true), DestructiveHint: ptr(false)},
	}, t.driftAccept)
	return server
}

func ptr[T any](v T) *T { return &v }

type tools struct {
	run Runner
	// check checks an import's arguments before the user is asked to
	// confirm it, so that what the command would refuse isn't confirmed.
	check Checker
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

// readWithin reads the file at path, which the agent chose, under the root
// (see within), and returns the path to use and the content. The file is
// opened once and read from that handle, which has to be the file path
// leads to under the root once it is open, so a link swapped in between
// the check and the open is refused; and the file may have no other name,
// as a hard link to a file outside the root would give it, where the
// platform counts them.
func (t *tools) readWithin(what, path string) (string, []byte, error) {
	path, err := t.within(what, path)
	if err != nil {
		return "", nil, err
	}
	// Before opening it, which would wait on a pipe.
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s %q isn't a regular file", what, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if !opened.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s %q isn't a regular file", what, path)
	}
	// Checked again now that it is open, so a link swapped in before the
	// open is caught, and the handle compared with where path leads.
	if _, err := t.within(what, path); err != nil {
		return "", nil, err
	}
	resolved, err := resolve(path)
	if err != nil {
		return "", nil, err
	}
	current, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !os.SameFile(opened, current) {
		return "", nil, fmt.Errorf("%s %q changed while it was opened", what, path)
	}
	if hardLinked(opened) {
		return "", nil, fmt.Errorf("%s %q has another name, a hard link, which could be outside %s", what, path, t.root)
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return "", nil, err
	}
	return path, content, nil
}

// Scope is what to list: a provider command, its services, and where to
// list them, in the provider's own terms. Each field but Provider sets the
// flag its flag tag names (see flagArgs). A field with a providers tag is
// for the providers it names alone, whose command has that flag: the
// tools reject it for the others (see checkScopeProviders), and the tools'
// descriptions say which provider takes which (see scopeHelp). The
// provider's description names the provider commands compiled in (see
// scopeSchema).
type Scope struct {
	Provider string   `json:"provider" jsonschema:"the provider command"`
	Services []string `json:"services,omitempty" flag:"services" jsonschema:"services to list, such as vpc, s3 and iam; with cfn_stacks, those of the stacks' resource types when left out"`
	// Regions is for aws and google.
	Regions []string `json:"regions,omitempty" flag:"regions" providers:"aws,google" jsonschema:"aws and google only: regions to list, such as eu-west-2 for aws or global and europe-west1 for google (default: the profile's region for aws, global for google)"`
	// Profile and Accounts are for aws.
	Profile  string   `json:"profile,omitempty" flag:"profile" providers:"aws" jsonschema:"aws only: the AWS profile whose credentials to use"`
	Accounts []string `json:"accounts,omitempty" flag:"accounts" providers:"aws" jsonschema:"aws only: accounts to list, each through the role infraharvest-readonly in it"`
	// CloudFormationStacks and CloudFormationIgnore are for aws: the
	// stacks whose resources alone to import, and those of their resources
	// Terraform can't import that they may be deleted without.
	CloudFormationStacks []string `json:"cfn_stacks,omitempty" flag:"cfn-stack,each" providers:"aws" jsonschema:"aws only: CloudFormation stacks, by name or stack ID (ARN), whose resources and nested stacks' alone to import; the report says whether each can be deleted once they are in Terraform"`
	CloudFormationIgnore []string `json:"cfn_ignore,omitempty" flag:"cfn-ignore,each" providers:"aws" jsonschema:"aws only, with cfn_stacks: resources of the stacks Terraform can't import, such as custom resources, by logical ID or as <stack>/<logical ID>, which the stacks may be deleted without"`
	// Projects, Organization and Folders are for google, which needs at
	// least one of them.
	Projects     []string `json:"projects,omitempty" flag:"projects" providers:"google" jsonschema:"google only: projects to list; google needs projects, organization or folders"`
	Organization string   `json:"organization,omitempty" flag:"organization" providers:"google" jsonschema:"google only: an organization ID, to list every active project of it, in its folders too"`
	Folders      []string `json:"folders,omitempty" flag:"folders" providers:"google" jsonschema:"google only: folder IDs, to list every active project of them, in their subfolders too"`
	// Subscriptions, ManagementGroup and ResourceGroup are for azure.
	Subscriptions   []string `json:"subscriptions,omitempty" flag:"subscriptions" providers:"azure" jsonschema:"azure only: subscription IDs to list (default: ARM_SUBSCRIPTION_ID's)"`
	ManagementGroup string   `json:"management_group,omitempty" flag:"management-group" providers:"azure" jsonschema:"azure only: a management group ID, to list every enabled subscription under it, in its child groups too"`
	ResourceGroup   string   `json:"resource_group,omitempty" flag:"resource-group" providers:"azure" jsonschema:"azure only: the resource group to list (default: the whole subscription)"`
	// Azapi is for azure.
	Azapi bool `json:"azapi,omitempty" flag:"azapi" providers:"azure" jsonschema:"azure only: import what Resource Graph lists that azurerm has no resource type for as azapi_resource, through the Azure/azapi provider, instead of leaving it out"`
}

// DiscoverInput is the discover tool's input.
type DiscoverInput struct {
	Scope
	Selection string `json:"selection" jsonschema:"the selection file to write, such as selection.yaml"`
	// Config is --config, checked as the other paths are.
	Config string `json:"config,omitempty" jsonschema:"a configuration file, as --config takes it, which sets what the other parameters don't, such as the services and regions"`
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
	args, err := commandArgs("discover", in.Scope, in)
	if err != nil {
		return nil, nil, err
	}
	if in.Selection == "" {
		return nil, nil, errors.New("selection is required")
	}
	if in.Selection, err = t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	config, err := t.loadConfig(&in.Config, configUse{tool: "discover", provider: in.Provider})
	if err != nil {
		return nil, nil, err
	}
	defer config.remove()
	args = append(args, config.flag()...)
	// discover saves what it lists under the output directory: the default
	// one, but under the root rather than where the server runs.
	out, err := t.within("out", report.DefaultOut)
	if err != nil {
		return nil, nil, err
	}
	if err := t.linksStayUnder("out", out); err != nil {
		return nil, nil, err
	}
	// Never the picker: the client talks to this process over stdio.
	args = append(args, "--pick=false", "--out="+out, "--selection="+in.Selection)
	_, stderr, code, err := t.run(ctx, config.run(args))
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
	s := f.Summary()
	summary := &SelectionSummary{Selection: path, Included: s.Included, Excluded: s.Excluded}
	for _, c := range s.Types {
		summary.Types = append(summary.Types, TypeCount(c))
	}
	for _, e := range s.Exclusions {
		if len(summary.Exclusions) == maxExclusions {
			summary.MoreExclusions++
			continue
		}
		summary.Exclusions = append(summary.Exclusions, fmt.Sprintf("%s %s: %s", e.Type, e.ID, e.Reason))
	}
	return summary, nil
}

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

func (t *tools) importSelection(ctx context.Context, req *mcp.CallToolRequest, in ImportInput) (*mcp.CallToolResult, *ImportResult, error) {
	args, err := commandArgs("import", in.Scope, in)
	if err != nil {
		return nil, nil, err
	}
	if in.Selection == "" || in.Out == "" {
		return nil, nil, errors.New("selection and out are required")
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
	// The check reads the private copy of it that the run reads.
	config, err := t.loadConfig(&in.Config, configUse{tool: "import", provider: in.Provider})
	if err != nil {
		return nil, nil, err
	}
	defer config.remove()
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
	scope, err := config.scope(in.Scope)
	if err != nil {
		return nil, nil, err
	}
	reads, err := scopeText(scope, providers.New(in.Provider).Profile())
	if err != nil {
		return nil, nil, err
	}
	message := fmt.Sprintf("Import the %d resources that %q includes (%d excluded) into Terraform configuration in %q? infraharvest reads %s and writes files; it never applies anything.",
		summary.Included, in.Selection, summary.Excluded, in.Out, reads)
	if in.Incremental {
		message = fmt.Sprintf("Add the resources that %q includes (%d excluded) and that the roots in %q don't have yet to those roots, after the blocks of the file of their type? What the roots have stays as it is. infraharvest reads %s and writes files; it never applies anything.",
			in.Selection, summary.Excluded, in.Out, reads)
	}
	if in.Regenerate {
		message = fmt.Sprintf("Generate the roots in %q again, from scratch, from the %d resources that %q includes (%d excluded)? Their files are written again, with moved blocks for resources at other addresses, and removed blocks for those that go to another root it generates; a root whose resources would go where neither can follow them, or be left out, is left as it was. infraharvest reads %s and writes files; it never applies anything.",
			in.Out, summary.Included, in.Selection, summary.Excluded, reads)
	}
	message += config.message()
	state := confirmationState(args, content, config.sum())
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

	childArgs := config.run(args)
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

		IncompleteHandovers: r.IncompleteHandovers,
		KeptRoots:           r.KeptRoots,
		Warnings:            warnings(&r),
	}
	text := fmt.Sprintf("Imported %d of %d resources into %s (exit code %d); %d left out, %d excluded, %d failed checks. The report is in %s.",
		r.Totals.Imported, r.Totals.Discovered, in.Out, code, r.Totals.LeftOut, r.Totals.Excluded, len(result.FailedChecks), result.Report)
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
	return textResult(text), result, nil
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

// confirmationState identifies a run the user is asked to confirm: its
// arguments and the whole content of the files it reads, such as its
// selection or decisions file and its configuration file, so an agent
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

// snapshot is a private copy of a file's content, such as a selection
// file's, at a path of its own named name.
type snapshot struct {
	dir, path string
}

func newSnapshot(name string, content []byte) (*snapshot, error) {
	dir, err := os.MkdirTemp("", "infraharvest-mcp-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
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
	// Kind is the report to read: reportImport or reportDrift.
	Kind string `json:"kind,omitempty" jsonschema:"import for the import's report (report.md), drift for the drift report (drift.md); default: the import's, or the drift report when the output directory has only that"`
}

// The kinds of report the report tool reads.
const (
	reportImport = "import"
	reportDrift  = "drift"
)

func (t *tools) report(_ context.Context, _ *mcp.CallToolRequest, in ReportInput) (*mcp.CallToolResult, any, error) {
	if in.Out == "" {
		return nil, nil, errors.New("out is required")
	}
	if in.Kind != "" && in.Kind != reportImport && in.Kind != reportDrift {
		return nil, nil, fmt.Errorf("kind must be %s or %s, not %q", reportImport, reportDrift, in.Kind)
	}
	out, err := t.within("out", in.Out)
	if err != nil {
		return nil, nil, err
	}
	in.Out = out
	path := filepath.Join(in.Out, report.Dir, "report.md")
	drift := filepath.Join(in.Out, report.Dir, report.DriftMarkdownFile)
	switch in.Kind {
	case reportDrift:
		path = drift
	case "":
		// An output directory drift checked, whose import ran elsewhere,
		// has the drift report only.
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			if _, err := os.Lstat(drift); err == nil {
				path = drift
			}
		}
	}
	// The report's own path too: .infraharvest or the report could be a
	// symbolic link that leads out of the root, such as one a cloned
	// repository plants to a credentials file.
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

// scopeHelp says which provider takes which of Scope's fields, for the
// tools' descriptions, from their providers tags, and which of them a
// provider's command needs one of, from its profile (see
// harvest.Scope.Needs).
var scopeHelp = func() string {
	scope := reflect.TypeFor[Scope]()
	var takes, needs []string
	for _, provider := range providers.Names() {
		var fields, needed []string
		required := providers.New(provider).Profile().Scope.Needs
		for i := range scope.NumField() {
			field := scope.Field(i)
			if !slices.Contains(scopeProviders(field), provider) {
				continue
			}
			fields = append(fields, jsonName(field))
			if flag, _, _ := strings.Cut(field.Tag.Get("flag"), ","); slices.Contains(required, flag) {
				needed = append(needed, jsonName(field))
			}
		}
		takes = append(takes, provider+" takes "+sentenceJoin(fields, "and"))
		if len(needed) > 0 {
			needs = append(needs, " The "+provider+" command needs "+sentenceJoin(needed, "or")+".")
		}
	}
	return "Where to list depends on the provider: " + strings.Join(takes, "; ") + "." + strings.Join(needs, "")
}()

// scopeProviders returns the providers that field, one of Scope's, is
// for, as its providers tag names them, or none for a field every
// provider takes.
func scopeProviders(field reflect.StructField) []string {
	tag, ok := field.Tag.Lookup("providers")
	if !ok {
		return nil
	}
	return strings.Split(tag, ",")
}

// jsonName returns the name an agent sends field by.
func jsonName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

// sentenceJoin joins words as a sentence lists them, with conjunction
// before the last: a, b and c.
func sentenceJoin(words []string, conjunction string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conjunction + " " + words[len(words)-1]
}

// scopeSchema returns the input schema of a tool whose input In embeds
// Scope, as the SDK would infer it, with the provider commands compiled
// in after the provider's description: the provider command: aws, azure
// or google. It panics where the schema can't be inferred, as the SDK
// does.
func scopeSchema[In any]() *jsonschema.Schema {
	schema, err := jsonschema.For[In](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("mcpserver: the input schema of %T: %v", *new(In), err))
	}
	provider := schema.Properties["provider"]
	provider.Description += ": " + sentenceJoin(providers.Names(), "or")
	return schema
}

// checkScopeProviders rejects each field of s that is set and is for
// other providers than s's, rather than leave it to a flag the command
// may have with another meaning: aws has --organization too, which lists
// the whole organisation.
func checkScopeProviders(s Scope) error {
	v := reflect.ValueOf(s)
	var errs []error
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		takers := scopeProviders(field)
		if len(takers) == 0 || !isSet(value) || slices.Contains(takers, s.Provider) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s doesn't take %s, which is for %s only", s.Provider, jsonName(field), strings.Join(takers, " and ")))
	}
	return errors.Join(errs...)
}

// isSet says whether value, a field of a tool's input, is set: a list
// with no values isn't.
func isSet(value reflect.Value) bool {
	if value.Kind() == reflect.Slice {
		return value.Len() > 0
	}
	return !value.IsZero()
}

// commandArgs returns the arguments of command on scope's provider, with
// the flags of in, a tool's input that has scope in it (see flagArgs). A
// field for other providers is rejected (see checkScopeProviders); what
// the provider's command makes of the rest, such as a Google scope
// without a project, is for the command to check: discover returns the
// error it runs into, and import checks them with the command's checks
// before the user is asked (see Checker).
func commandArgs(command string, scope Scope, in any) ([]string, error) {
	// The provider is the one argument that isn't a --flag=value: one the
	// binary doesn't have would be taken for something else, such as a
	// flag.
	if err := providers.Check(scope.Provider); err != nil {
		return nil, err
	}
	if err := checkScopeNames(scope); err != nil {
		return nil, err
	}
	if err := checkScopeProviders(scope); err != nil {
		return nil, err
	}
	flags, err := flagArgs(in)
	if err != nil {
		return nil, err
	}
	return append([]string{command, scope.Provider}, flags...), nil
}

// flagArgs returns the flags that in, a tool's input, sets: for each field
// with a flag tag, in this one place, the flag that tag names, with the
// field's value, or none when the field isn't set. A list goes into one
// flag, comma-separated, so that a value of it with a comma, which the
// command would take for two, is rejected; or with ",each" in the tag, as
// a flag that takes one value each time such as --cfn-stack, into a flag
// per value. An empty value in a list is rejected. Every value is part of
// a --flag=value argument, so none can become a flag of its own; one with
// a control character, such as a newline, which could make the
// confirmation the user is shown say something else, is rejected. The
// fields of an embedded struct come in its place.
func flagArgs(in any) ([]string, error) {
	var args []string
	v := reflect.ValueOf(in)
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		if field.Anonymous {
			embedded, err := flagArgs(value.Interface())
			if err != nil {
				return nil, err
			}
			args = append(args, embedded...)
			continue
		}
		tag, ok := field.Tag.Lookup("flag")
		if !ok {
			continue
		}
		name, mode, _ := strings.Cut(tag, ",")
		var values []string
		switch value.Kind() {
		case reflect.Bool:
			if value.Bool() {
				args = append(args, "--"+name)
			}
			continue
		case reflect.Int:
			if value.Int() != 0 {
				values = []string{strconv.FormatInt(value.Int(), 10)}
			}
		case reflect.String:
			if value.String() != "" {
				values = []string{value.String()}
			}
		case reflect.Slice:
			values = value.Interface().([]string)
			for _, v := range values {
				if v == "" {
					return nil, fmt.Errorf("%s has an empty value", jsonName(field))
				}
				if mode != "each" && strings.Contains(v, ",") {
					return nil, fmt.Errorf("%s %q has a comma", jsonName(field), v)
				}
			}
			if len(values) > 0 && mode != "each" {
				values = []string{strings.Join(values, ",")}
			}
		}
		for _, value := range values {
			if strings.ContainsFunc(value, unicode.IsControl) {
				return nil, fmt.Errorf("%s %q has a control character", jsonName(field), value)
			}
			args = append(args, "--"+name+"="+value)
		}
	}
	return args, nil
}

// namePattern is what a service or a region is named like: vpc, s3,
// resource_graph, eu-west-2, global, or * for all.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_*.-]+$`)

// checkScopeNames rejects a service or region that isn't named like one,
// which the confirmation the user is shown names.
func checkScopeNames(s Scope) error {
	for _, v := range append(slices.Clone(s.Services), s.Regions...) {
		if !namePattern.MatchString(v) {
			return fmt.Errorf("%q isn't a service or a region", v)
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

// scopeText says what an import reads, for the user to confirm: what
// profile, the profile of scope's provider, describes of the flags scope
// sets (see harvest.Scope.Describe), the provider's accounts,
// subscriptions or projects and where in them, in the provider's own
// terms; and the services, which, when scope names none, the stacks'
// resources do. scope is the run's: the tool's input, with what its
// configuration file sets of the rest (see toolConfig.scope), and each
// field set is one its provider takes.
func scopeText(s Scope, profile harvest.Profile) (string, error) {
	if profile.Scope.Describe == nil {
		return "", fmt.Errorf("can't say what provider %q reads", s.Provider)
	}
	text := profile.Scope.Describe(scopeFlags(s))
	if len(s.Services) == 0 {
		return text + " (the services of the stacks' resources)", nil
	}
	return text + " (" + strings.Join(s.Services, ", ") + ")", nil
}

// scopeFlags returns the flags of s that are set, by the names their flag
// tags give them, with their values, as a provider's profile describes
// them (see harvest.Scope.Describe): a flag that takes no value has true.
func scopeFlags(s Scope) map[string][]string {
	flags := map[string][]string{}
	v := reflect.ValueOf(s)
	for i := range v.NumField() {
		field, value := v.Type().Field(i), v.Field(i)
		tag, ok := field.Tag.Lookup("flag")
		if !ok || !isSet(value) {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		switch value.Kind() {
		case reflect.Bool:
			flags[name] = []string{"true"}
		case reflect.String:
			flags[name] = []string{value.String()}
		case reflect.Slice:
			flags[name] = value.Interface().([]string)
		}
	}
	return flags
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

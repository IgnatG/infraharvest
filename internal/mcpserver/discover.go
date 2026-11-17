// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

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

func (t *tools) discover(ctx context.Context, req *mcp.CallToolRequest, in DiscoverInput) (*mcp.CallToolResult, *SelectionSummary, error) {
	args, err := t.providers.commandArgs("discover", in.Scope, in)
	if err != nil {
		return nil, nil, err
	}
	if in.Selection == "" {
		return nil, nil, errors.New("selection is required")
	}
	if in.Selection, err = t.within("selection", in.Selection); err != nil {
		return nil, nil, err
	}
	config, err := t.loadConfig(&in.Config, configUse{tool: "discover", provider: in.Provider, providers: t.providers})
	if err != nil {
		return nil, nil, err
	}
	defer config.remove()
	scope, err := config.scope(in.Scope)
	if err != nil {
		return nil, nil, err
	}
	if err := checkQueryEngine(scope, config.engine("")); err != nil {
		return nil, nil, err
	}
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
	_, stderr, code, err := t.runReporting(ctx, req, config.run(args))
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

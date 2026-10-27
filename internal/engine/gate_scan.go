// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// nondeterministic are functions whose results differ between runs.
var nondeterministic = map[string]bool{"timestamp": true, "plantimestamp": true, "uuid": true, "uuidv5": true, "bcrypt": true}

// scanNondeterminism checks the configuration Generate wrote, with the
// local modules it calls (see walkOutputAndModules), calls no
// function whose result differs between runs, so that importing an
// unchanged estate again gives the same files.
func scanNondeterminism(dir string) (Check, error) {
	check := Check{Name: CheckDeterminism, Passed: true}
	err := walkOutputAndModules(dir, func(rel string, content string) {
		for _, call := range nondeterministicCalls(rel, content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": calls "+call+"(")
		}
	})
	return check, err
}

// nondeterministicCalls returns the nondeterministic functions a .tf
// file's expressions call, each once, sorted. Strings and comments that
// only mention a function's name don't count.
func nondeterministicCalls(rel, content string) []string {
	if !strings.HasSuffix(rel, ".tf") {
		return nil
	}
	// A file that doesn't parse has nothing to scan, since validate (G2)
	// reports it.
	f, err := hclfile.Parse(rel, []byte(content))
	if err != nil {
		return nil
	}
	calls := map[string]bool{}
	_ = hclsyntax.VisitAll(f.Syntax, func(node hclsyntax.Node) hcl.Diagnostics {
		if call, ok := node.(*hclsyntax.FunctionCallExpr); ok && nondeterministic[call.Name] {
			calls[call.Name] = true
		}
		return nil
	})
	return sortedKeys(calls)
}

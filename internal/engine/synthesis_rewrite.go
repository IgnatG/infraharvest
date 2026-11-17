// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// referenceRename makes the references that begin with the steps of search
// begin with replacement instead (see replaceReferences).
type referenceRename struct {
	search      []string
	replacement hclwrite.Tokens
}

// replaceAllReferences applies renames to body and its nested blocks, in
// order: those byStart, their index (see indexRenames), finds in body.
func replaceAllReferences(body *hclwrite.Body, renames []referenceRename, byStart renameIndex) {
	for _, i := range byStart.in(body) {
		replaceReferences(body, renames[i].search, renames[i].replacement)
	}
}

// renameIndex is a list of renames by the first two steps of what they
// search for, aws_vpc.main for aws_vpc.main.id, so that a body is handed
// only the renames of the references it has: a root's renames are for
// every resource moved into a module, and each resource refers to a few.
// The renames whose search has fewer steps are under "".
type renameIndex map[string][]int

// indexRenames indexes n renames, whose searches search returns.
func indexRenames(n int, search func(i int) []string) renameIndex {
	idx := renameIndex{}
	for i := range n {
		key := ""
		if s := search(i); len(s) >= 2 {
			key = s[0] + "." + s[1]
		}
		idx[key] = append(idx[key], i)
	}
	return idx
}

// in returns the indexes, in order, of the renames that may have something
// to replace in body and its nested blocks: those whose search begins with
// two steps a reference in body begins with, an identifier that doesn't
// follow a dot, a dot and an identifier, as spliceTraversal and
// RenameVariablePrefix match them. A rename's replacement never begins
// with what another searches for, a resource type, so the references
// body has before the renames are those it has between them.
func (idx renameIndex) in(body *hclwrite.Body) []int {
	found := slices.Clone(idx[""])
	var visit func(body *hclwrite.Body)
	seen := map[string]bool{}
	visit = func(body *hclwrite.Body) {
		for _, attr := range body.Attributes() {
			tokens := attr.Expr().BuildTokens(nil)
			for i := 0; i+2 < len(tokens); i++ {
				if tokens[i].Type != hclsyntax.TokenIdent || (i > 0 && tokens[i-1].Type == hclsyntax.TokenDot) ||
					tokens[i+1].Type != hclsyntax.TokenDot || tokens[i+2].Type != hclsyntax.TokenIdent {
					continue
				}
				key := string(tokens[i].Bytes) + "." + string(tokens[i+2].Bytes)
				if !seen[key] {
					seen[key] = true
					found = append(found, idx[key]...)
				}
			}
		}
		for _, b := range body.Blocks() {
			visit(b.Body())
		}
	}
	visit(body)
	slices.Sort(found)
	return found
}

// replaceReferences replaces, in body and its nested blocks, the start of
// every reference that begins with the steps of search, such as
// aws_s3_bucket.logs.arn, with replacement.
func replaceReferences(body *hclwrite.Body, search []string, replacement hclwrite.Tokens) {
	for name, attr := range body.Attributes() {
		tokens := attr.Expr().BuildTokens(nil)
		if replaced, ok := spliceTraversal(tokens, search, replacement); ok {
			body.SetAttributeRaw(name, replaced)
		}
	}
	for _, b := range body.Blocks() {
		replaceReferences(b.Body(), search, replacement)
	}
}

// spliceTraversal replaces each run of tokens that spells the steps of
// search, a.b.c, at the start of a traversal, with replacement. It reports
// whether it replaced any.
func spliceTraversal(tokens hclwrite.Tokens, search []string, replacement hclwrite.Tokens) (hclwrite.Tokens, bool) {
	n := 2*len(search) - 1
	var out hclwrite.Tokens
	replaced := false
	for i := 0; i < len(tokens); i++ {
		if i+n <= len(tokens) && (i == 0 || tokens[i-1].Type != hclsyntax.TokenDot) && spells(tokens[i:i+n], search) {
			for j, tok := range replacement {
				copied := *tok
				if j == 0 {
					copied.SpacesBefore = tokens[i].SpacesBefore
				}
				out = append(out, &copied)
			}
			replaced = true
			i += n - 1
			continue
		}
		out = append(out, tokens[i])
	}
	return out, replaced
}

// spells reports whether tokens are the identifiers of steps, joined by
// dots.
func spells(tokens hclwrite.Tokens, steps []string) bool {
	for i, step := range steps {
		ident := tokens[2*i]
		if ident.Type != hclsyntax.TokenIdent || string(ident.Bytes) != step {
			return false
		}
		if i > 0 && tokens[2*i-1].Type != hclsyntax.TokenDot {
			return false
		}
	}
	return true
}

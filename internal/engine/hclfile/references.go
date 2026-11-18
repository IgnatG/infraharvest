// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// ReplaceReferences replaces, in body and its nested blocks, the start of
// every reference that begins with the steps of search, such as
// aws_s3_bucket.logs.arn, with replacement.
func ReplaceReferences(body *hclwrite.Body, search []string, replacement hclwrite.Tokens) {
	for name, attr := range body.Attributes() {
		tokens := attr.Expr().BuildTokens(nil)
		if replaced, ok := spliceTraversal(tokens, search, replacement); ok {
			body.SetAttributeRaw(name, replaced)
		}
	}
	for _, b := range body.Blocks() {
		ReplaceReferences(b.Body(), search, replacement)
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

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// notePrincipals notes, after each argument in the file at path that the
// catalogue says names an object of the cloud's directory (see
// catalogue.Principal) and that is still a literal, which kind of object
// it is, such as an Entra ID group for an azurerm role assignment's
// principal_id: what the role is assigned to wasn't imported, as no
// resource of the root is that object (see nestedIdentities), and the
// directory's objects belong to another provider, which an import doesn't
// use. The kind is the catalogue's for the value of the resource's
// argument that says it, where the resource sets one, and object
// otherwise. A comment doesn't change the plan. It reports whether it
// noted any.
func notePrincipals(path string) (bool, error) {
	return rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		changed := false
		blocks := f.Body().Blocks()
		for i, b := range syntax.Blocks {
			if b.Type != "resource" || len(b.Labels) != 2 || i >= len(blocks) {
				continue
			}
			p, ok := catalogue.ForType(b.Labels[0]).Principal(b.Labels[0])
			if !ok {
				continue
			}
			attr, ok := b.Body.Attributes[p.Argument]
			if !ok {
				continue
			}
			tmpl, ok := attr.Expr.(*hclsyntax.TemplateExpr)
			if !ok {
				continue
			}
			if _, ok := literalString(tmpl); !ok {
				continue
			}
			// The mark of a note, so that an argument is noted once.
			mark := "# " + p.Directory + " "
			body := blocks[i].Body()
			written := body.GetAttribute(p.Argument)
			if written == nil || strings.Contains(string(written.BuildTokens(nil).Bytes()), mark) {
				continue
			}
			kind := "object"
			if t, ok := b.Body.Attributes[p.KindArgument]; ok && p.KindArgument != "" {
				if tmpl, ok := t.Expr.(*hclsyntax.TemplateExpr); ok {
					if v, ok := literalString(tmpl); ok && p.Kinds[v] != "" {
						kind = p.Kinds[v]
					}
				}
			}
			// The comment ends the line the value is on: hclwrite.Format,
			// which saving runs, reads it as the attribute's line comment.
			tokens := append(written.Expr().BuildTokens(nil), &hclwrite.Token{
				Type:  hclsyntax.TokenComment,
				Bytes: []byte(mark + kind + ": " + p.Note),
			})
			body.SetAttributeRaw(p.Argument, tokens)
			changed = true
		}
		return changed
	})
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// principalKinds name, by an azurerm role assignment's principal_type,
// what its principal_id is in Entra ID.
var principalKinds = map[string]string{
	"User":             "user",
	"Group":            "group",
	"ForeignGroup":     "foreign group",
	"ServicePrincipal": "service principal",
	"Device":           "device",
}

// entraNote marks the principals notePrincipals has noted, so that a
// principal is noted once.
const entraNote = "# Entra ID "

// notePrincipals notes, after the principal_id of each azurerm role
// assignment in the file at path that is still a literal, that it is an
// Entra ID object: what the role is assigned to wasn't imported, as no
// resource of the root has that principal (see nestedIdentities), and
// users, groups and applications belong to the azuread provider, which an
// import doesn't use. The note says what kind of object it is, by the
// assignment's principal_type, where the assignment sets one. A comment
// doesn't change the plan. It reports whether it noted any.
func notePrincipals(path string) (bool, error) {
	return rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		changed := false
		blocks := f.Body().Blocks()
		for i, b := range syntax.Blocks {
			if b.Type != "resource" || len(b.Labels) != 2 || b.Labels[0] != "azurerm_role_assignment" || i >= len(blocks) {
				continue
			}
			attr, ok := b.Body.Attributes["principal_id"]
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
			body := blocks[i].Body()
			written := body.GetAttribute("principal_id")
			if written == nil || strings.Contains(string(written.BuildTokens(nil).Bytes()), entraNote) {
				continue
			}
			kind := "object"
			if t, ok := b.Body.Attributes["principal_type"]; ok {
				if tmpl, ok := t.Expr.(*hclsyntax.TemplateExpr); ok {
					if v, ok := literalString(tmpl); ok && principalKinds[v] != "" {
						kind = principalKinds[v]
					}
				}
			}
			// The comment ends the line the value is on: hclwrite.Format,
			// which saving runs, reads it as the attribute's line comment.
			tokens := append(written.Expr().BuildTokens(nil), &hclwrite.Token{
				Type:  hclsyntax.TokenComment,
				Bytes: []byte(entraNote + kind + ": users, groups and applications belong to the azuread provider"),
			})
			body.SetAttributeRaw("principal_id", tokens)
			changed = true
		}
		return changed
	})
}

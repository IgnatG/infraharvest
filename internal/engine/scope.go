// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"maps"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// ScopeLocals is what a root reads of its own account (or subscription,
// or project) and of its own region, as a root environments share reads
// them (see envmerge.MergeProfile): the literals that hold them become
// references to locals of data sources (see liftScope). A part without
// an ID stays literal, such as the region of a root of global services,
// whose provider is in a region that isn't the root's.
type ScopeLocals struct {
	Account, Region ScopeValue
}

// ScopeValue is one ID a root's literals hold: ID is the value, such as
// 123456789012 or eu-west-2, "" for none; Data the data source that reads
// it, such as aws_caller_identity.current; Local the local that holds it,
// such as aws_account_id; and Expr the expression that reads it, such as
// data.aws_caller_identity.current.account_id.
type ScopeValue struct {
	ID, Data, Local, Expr string
}

// set reports whether v has an ID and says how to read it.
func (v ScopeValue) set() bool {
	return v.ID != "" && v.Data != "" && v.Local != "" && v.Expr != ""
}

// scopeAnnotation is the annotation that makes generated.tf read the
// root's own account and region from locals (see liftScope).
func scopeAnnotation(ws *workspace, s *ScopeLocals, taken Names, keep map[string]bool) annotation {
	return annotation{name: "scope read from data sources", edit: func(f *hclfile.File) (bool, error) {
		return liftScope(ws, f, s, taken, keep)
	}}
}

// liftScope makes each string value of generated, the generated.tf of
// ws, and of the literal locals of locals.tf, that holds the root's own
// account or region of s as a whole word (see scopeWordIndex) read it
// from its local, as a root environments share does:
// "arn:aws:lambda:${local.aws_region}:${local.aws_account_id}:function:app",
// and account_id = local.aws_account_id where it is the whole value. The
// locals it uses go to locals.tf, and their data sources, which have no
// arguments, to data.tf: a root with nothing to lift gets none. The
// values stay the same, and the data sources are read when the root
// plans, so the plan does too. Arguments named in keep, such as tags,
// a module call's source and version, and object keys stay literal, as
// do the import blocks, variables and data sources. A part whose local or data source the root, or taken,
// has for something else stays literal too. It reports whether it
// changed generated.
func liftScope(ws *workspace, generated *hclfile.File, s *ScopeLocals, taken Names, keep map[string]bool) (bool, error) {
	if s == nil {
		return false, nil
	}
	locals, err := ws.LoadOptional(rootfile.LocalsFileName)
	if err != nil {
		return false, err
	}
	data, err := ws.LoadOptional(rootfile.DataFileName)
	if err != nil {
		return false, err
	}
	var values []ScopeValue
	own := map[string]bool{}
	for _, v := range []ScopeValue{s.Account, s.Region} {
		if v.set() && scopeLocalFree(v, locals, data, taken) {
			values = append(values, v)
			own[v.Local] = true
		}
	}
	if len(values) == 0 {
		return false, nil
	}
	used := map[string]bool{}
	changed := false
	blocks := generated.Edit().Body().Blocks()
	// A module call's source and version must be literals.
	call := map[string]bool{"source": true, "version": true}
	maps.Copy(call, keep)
	for _, b := range blocks {
		switch b.Type() {
		case "resource":
			changed = liftScopeIn(b.Body(), values, keep, used) || changed
		case "module":
			changed = liftScopeIn(b.Body(), values, call, used) || changed
		}
	}
	localsChanged := false
	if locals != nil {
		for _, b := range locals.Edit().Body().Blocks() {
			if b.Type() != "locals" {
				continue
			}
			for name, attr := range b.Body().Attributes() {
				if own[name] || keep[name] {
					continue
				}
				if out, ok := liftScopeTokens(attr.Expr().BuildTokens(nil), values, used); ok {
					b.Body().SetAttributeRaw(name, out)
					localsChanged = true
				}
			}
		}
	}
	if !changed && !localsChanged {
		return false, nil
	}
	var add []*hclwrite.Block
	for _, v := range values {
		if used[v.Local] && !declaresData(data, v.Data) {
			typ, name, _ := strings.Cut(v.Data, ".")
			add = append(add, hclwrite.NewBlock("data", []string{typ, name}))
		}
	}
	if err := appendBlocks(ws, rootfile.DataFileName, rootfile.DataFileHeader, add); err != nil {
		return false, err
	}
	if locals == nil {
		locals = hclfile.New(ws.path(rootfile.LocalsFileName))
	}
	var body *hclwrite.Body
	for _, b := range locals.Edit().Body().Blocks() {
		if b.Type() == "locals" {
			body = b.Body()
			break
		}
	}
	if body == nil {
		body = locals.Edit().Body().AppendNewBlock("locals", nil).Body()
	}
	for _, v := range values {
		if used[v.Local] && body.GetAttribute(v.Local) == nil {
			body.SetAttributeRaw(v.Local, hclfile.ParseExpr(v.Expr))
		}
	}
	ws.Save(locals)
	return changed, nil
}

// scopeLocalFree reports whether the root can read v from its local:
// locals.tf has no local of its name, or has it as v.Expr; and taken has
// no local of its name, unless it has v's data source too, as the root an
// incremental import adds to has once it read v the same way.
func scopeLocalFree(v ScopeValue, locals, data *hclfile.File, taken Names) bool {
	if locals != nil {
		for _, b := range locals.Syntax.Blocks {
			if attr, ok := b.Body.Attributes[v.Local]; ok && b.Type == "locals" {
				return strings.TrimSpace(string(attr.Expr.Range().SliceBytes(locals.Src))) == v.Expr
			}
		}
	}
	if taken["local."+v.Local] && !taken["data."+v.Data] {
		return false
	}
	if data != nil {
		for _, b := range data.Syntax.Blocks {
			if b.Type == "data" && len(b.Labels) == 2 && b.Labels[0]+"."+b.Labels[1] == v.Data {
				// One with arguments of its own reads something else.
				return len(b.Body.Attributes) == 0 && len(b.Body.Blocks) == 0
			}
		}
	}
	return true
}

// declaresData reports whether data, data.tf or nil, declares the data
// source at address, type.name.
func declaresData(data *hclfile.File, address string) bool {
	if data == nil {
		return false
	}
	for _, b := range data.Syntax.Blocks {
		if b.Type == "data" && len(b.Labels) == 2 && b.Labels[0]+"."+b.Labels[1] == address {
			return true
		}
	}
	return false
}

// liftScopeIn lifts the values of body's arguments, and of its nested
// blocks', but for those named in keep (see liftScopeTokens), adding
// the locals they then read to used. It reports whether it changed any.
func liftScopeIn(body *hclwrite.Body, values []ScopeValue, keep map[string]bool, used map[string]bool) bool {
	changed := false
	for name, attr := range body.Attributes() {
		if keep[name] {
			continue
		}
		if out, ok := liftScopeTokens(attr.Expr().BuildTokens(nil), values, used); ok {
			body.SetAttributeRaw(name, out)
			changed = true
		}
	}
	for _, b := range body.Blocks() {
		changed = liftScopeIn(b.Body(), values, keep, used) || changed
	}
	return changed
}

// liftScopeTokens returns tokens, an expression, with each whole-word
// occurrence of a value's ID in its strings read from the value's local,
// "${local.<name>}", or local.<name> alone where it is the whole string;
// an object key stays as it is. It adds the locals it reads to used, and
// reports whether it changed anything.
func liftScopeTokens(tokens hclwrite.Tokens, values []ScopeValue, used map[string]bool) (hclwrite.Tokens, bool) {
	out := make(hclwrite.Tokens, 0, len(tokens))
	changed := false
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.Type != hclsyntax.TokenQuotedLit && t.Type != hclsyntax.TokenStringLit {
			out = append(out, t)
			continue
		}
		whole := t.Type == hclsyntax.TokenQuotedLit && i > 0 && tokens[i-1].Type == hclsyntax.TokenOQuote && i+1 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenCQuote
		if whole && i+2 < len(tokens) && (tokens[i+2].Type == hclsyntax.TokenEqual || tokens[i+2].Type == hclsyntax.TokenColon) {
			// An object's key.
			out = append(out, t)
			continue
		}
		s := string(t.Bytes)
		var read []string
		for _, v := range values {
			var found bool
			if s, found = liftScopeWord(s, v.ID, "${local."+v.Local+"}"); found {
				read = append(read, v.Local)
			}
		}
		if len(read) == 0 {
			out = append(out, t)
			continue
		}
		changed = true
		for _, name := range read {
			used[name] = true
		}
		if whole && len(read) == 1 && s == "${local."+read[0]+"}" {
			// The whole string: the reference alone, in place of the quotes.
			open := out[len(out)-1]
			out = out[:len(out)-1]
			ref := hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: read[0]}})
			ref[0].SpacesBefore = open.SpacesBefore
			out = append(out, ref...)
			i++ // the closing quote
			continue
		}
		c := *t
		c.Bytes = []byte(s)
		out = append(out, &c)
	}
	return out, changed
}

// liftScopeWord returns s with each whole-word occurrence of id (see
// scopeWordIndex) replaced by with, and whether it had one.
func liftScopeWord(s, id, with string) (string, bool) {
	var b strings.Builder
	found := false
	for {
		i := scopeWordIndex(s, id)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		b.WriteString(with)
		s = s[i+len(id):]
		found = true
	}
	if !found {
		return s, false
	}
	b.WriteString(s)
	return b.String(), true
}

// scopeWordIndex returns the index of the first occurrence of id in s that
// is a whole word, as merging finds an environment's account and region
// in values: with no letter or digit next to it, so that
// arn:aws:iam::123456789012:role/ci holds the account and eu-west-2a
// doesn't hold the region eu-west-2. One after $ or %, which would make a
// template sequence, doesn't count. It returns -1 for none.
func scopeWordIndex(s, id string) int {
	if id == "" {
		return -1
	}
	for from := 0; from <= len(s); {
		i := strings.Index(s[from:], id)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(id)
		before := i == 0 || (!isScopeWordByte(s[i-1]) && s[i-1] != '$' && s[i-1] != '%')
		after := end == len(s) || !isScopeWordByte(s[end])
		if before && after {
			return i
		}
		from = i + 1
	}
	return -1
}

// isScopeWordByte reports whether c is an ASCII letter or digit.
func isScopeWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

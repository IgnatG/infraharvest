// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"bytes"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// insertArgument adds name = text at the end of block's body, which the
// block's layout then puts in its place (see hclfile.LayoutBody); nil if the
// block is on one line with something in it.
func (w *driftWalk) insertArgument(block *hclsyntax.Block, name string, text []byte) *hclfile.Edit {
	src := w.f.Src
	line := slices.Concat([]byte(name+" = "), text, []byte("\n"))
	opening, closing := block.OpenBraceRange, block.CloseBraceRange
	if opening.Start.Line == closing.Start.Line {
		if len(block.Body.Attributes)+len(block.Body.Blocks) > 0 {
			return nil
		}
		return &hclfile.Edit{File: w.file, Start: opening.End.Byte, End: closing.Start.Byte, Text: slices.Concat([]byte("\n"), line), Layout: w.address}
	}
	at := hclfile.LineStart(src, closing.Start.Byte)
	if len(bytes.TrimSpace(src[at:closing.Start.Byte])) > 0 {
		return nil
	}
	return &hclfile.Edit{File: w.file, Start: at, End: at, Text: line, Layout: w.address}
}

// insertKey adds the key with the cloud's value to obj, an object
// literal.
func (w *driftWalk) insertKey(obj *hclsyntax.ObjectConsExpr, key string, path []string, i int) codeSource {
	if w.c.value == nil {
		return w.manual(sourceLiteral, obj.Range(), "the code doesn't have the key the cloud removed")
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.unwritable(sourceLiteral, obj.Range())
	}
	item := keyText(key) + " = " + string(text)
	src := w.f.Src
	rng := obj.Range()
	end := rng.End.Byte - 1 // the closing brace
	var edit *hclfile.Edit
	switch {
	case obj.OpenRange.Start.Line == rng.End.Line && len(obj.Items) == 0:
		edit = &hclfile.Edit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: []byte("{\n" + item + "\n}")}
	case obj.OpenRange.Start.Line == rng.End.Line:
		after := obj.Items[len(obj.Items)-1].ValueExpr.Range().End.Byte
		edit = &hclfile.Edit{File: w.file, Start: after, End: after, Text: []byte(", " + item)}
	default:
		at := hclfile.LineStart(src, end)
		if len(bytes.TrimSpace(src[at:end])) > 0 {
			return w.manual(sourceLiteral, rng, "add the key by hand")
		}
		edit = &hclfile.Edit{File: w.file, Start: at, End: at, Text: []byte(item + "\n")}
	}
	return codeSource{File: w.file, Range: rng, Kind: sourceLiteral, Edit: edit}
}

// appendMergeArg adds { key = value } as the last argument of call, a
// merge, so that the resource's value wins.
func (w *driftWalk) appendMergeArg(call *hclsyntax.FunctionCallExpr, key string, path []string, i int) codeSource {
	if w.c.value == nil {
		return w.manual(sourceExpression, call.Range(), "merge() sets it, and the cloud removed the key: edit it by hand")
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.unwritable(sourceExpression, call.Range())
	}
	at := call.CloseParenRange.Start.Byte
	return codeSource{File: w.file, Range: call.Range(), Kind: sourceLiteral, Edit: &hclfile.Edit{File: w.file, Start: at, End: at, Text: []byte(", { " + keyText(key) + " = " + string(text) + " }")}}
}

// removeLines removes the lines of rng, if nothing else is on them.
func (w *driftWalk) removeLines(rng hcl.Range) *hclfile.Edit {
	src := w.f.Src
	start, end := hclfile.LineStart(src, rng.Start.Byte), hclfile.LineEnd(src, rng.End.Byte)
	if len(bytes.TrimSpace(src[start:rng.Start.Byte])) > 0 || len(bytes.TrimSpace(src[rng.End.Byte:end])) > 0 {
		return nil
	}
	return &hclfile.Edit{File: w.file, Start: start, End: end}
}

// removeItem removes item from obj: its lines, if it is alone on them,
// else the item and the comma after it.
func (w *driftWalk) removeItem(obj *hclsyntax.ObjectConsExpr, item *hclsyntax.ObjectConsItem) *hclfile.Edit {
	rng := hcl.RangeBetween(item.KeyExpr.Range(), item.ValueExpr.Range())
	if edit := w.removeLines(rng); edit != nil && obj.OpenRange.Start.Line != rng.Start.Line {
		return edit
	}
	src := w.f.Src
	end := rng.End.Byte
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	if end < len(src) && src[end] == ',' {
		end++
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
	}
	return &hclfile.Edit{File: w.file, Start: rng.Start.Byte, End: end}
}

// apply makes in contents, files by name, the edits that take the
// cloud's values for changes, in order, each found again in the files as
// the edits before it left them; each edited file is laid out where an
// argument was added, and formatted, as terraform fmt would. A change's
// edits, its Edit and those Also has, are made together, or none of them.
// It returns the names of the files it edited, sorted, the sources it
// edited by, and the sources of the changes it couldn't edit: a hand
// edit, or a secret, which includes an edit that would add a value the
// literal secrets check takes for one (see secret.Literals).
func (e *driftEditor) apply(contents map[string][]byte, changes []*Change) ([]string, map[*Change]codeSource, map[*Change]codeSource, error) {
	edited := map[string]bool{}
	applied := map[*Change]codeSource{}
	failed := map[*Change]codeSource{}
	var cfg *driftConfig
	// The parse of each file as the change before checked it, so that it
	// isn't parsed again for the next one.
	parses := parseCache{}
	for _, c := range changes {
		// Only the files the change before edited are parsed again.
		e.syncModules(contents)
		var err error
		if cfg, err = cfg.reparseWith(contents, parses); err != nil {
			return nil, nil, nil, err
		}
		src := e.resolve(cfg, c)
		if src.Edit == nil {
			failed[c] = src
			continue
		}
		next, reason, secret := editFiles(contents, slices.Concat([]hclfile.Edit{*src.Edit}, src.Also), parses)
		if reason != "" {
			src.Edit, src.Also, src.Reason, src.Secret = nil, nil, reason, secret
			failed[c] = src
			continue
		}
		for name, content := range next {
			contents[name] = content
			edited[name] = true
		}
		applied[c] = src
	}
	return slices.Sorted(maps.Keys(edited)), applied, failed, nil
}

// editFiles returns the files of contents edits change, as they edit
// them, each laid out where an argument was added and formatted; or why
// they can't be made, as an edited file wouldn't parse or would hold a
// literal secret it didn't (secret). It parses the files through
// parses, which keeps an edited file's parse for the next change.
func editFiles(contents map[string][]byte, edits []hclfile.Edit, parses parseCache) (map[string][]byte, string, bool) {
	byFile := map[string][]hclfile.Edit{}
	for _, edit := range edits {
		byFile[edit.File] = append(byFile[edit.File], edit)
	}
	next := map[string][]byte{}
	for _, name := range slices.Sorted(maps.Keys(byFile)) {
		old := contents[name]
		fileEdits := byFile[name]
		// From the last, so that the offsets of the others hold.
		sort.SliceStable(fileEdits, func(i, j int) bool { return fileEdits[i].Start > fileEdits[j].Start })
		content := old
		var layouts []string
		for _, edit := range fileEdits {
			content = slices.Concat(content[:edit.Start], edit.Text, content[edit.End:])
			if edit.Layout != "" {
				layouts = append(layouts, edit.Layout)
			}
		}
		for _, address := range layouts {
			content = layoutResource(content, address)
		}
		content = hclwrite.Format(content)
		// The file as it was first: its parse, which the change was
		// resolved in, is the one kept until the edited one is.
		var had gate.Check
		if f, err := parses.parse(name, old); err == nil {
			had.Details = parses.literals(name, f)
		}
		edited, err := parses.parse(name, content)
		if err != nil {
			return nil, "the edit wouldn't parse: edit it by hand", false
		}
		// The findings, not their count: an edit may remove one secret and
		// add another.
		if len(gate.AddedDetails(had, gate.Check{Details: parses.literals(name, edited)})) > 0 {
			return nil, "a secret, which is never written into the code: move it into a sensitive variable by hand, and set that from your secret store", true
		}
		next[name] = content
	}
	return next, "", false
}

// layoutResource lays out the block of the resource at address in src
// (see hclfile.LayoutBody), and only that block.
func layoutResource(src []byte, address string) []byte {
	f, err := hclfile.Parse("", src)
	if err != nil {
		return src
	}
	for _, b := range f.Syntax.Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 || b.Labels[0]+"."+b.Labels[1] != address || b.OpenBraceRange.Start.Line == b.CloseBraceRange.Start.Line {
			continue
		}
		inner, ok := hclfile.LayoutBody(src, b, true)
		if !ok {
			return src
		}
		return slices.Concat(src[:b.OpenBraceRange.End.Byte], []byte("\n"), inner, src[b.CloseBraceRange.Start.Byte:])
	}
	return src
}

// reportSource is src as a report shows it: the file and line, the
// expression if on one line, with secrets left out, and how a module gets
// the value from the call; nil without a file.
func reportSource(cfg *driftConfig, src codeSource, forms []string) *Source {
	if src.File == "" {
		return nil
	}
	out := &Source{File: src.File, Line: src.Range.Start.Line, ModulePath: src.Module}
	f := cfg.files[src.File]
	if f == nil || src.Kind == sourceAbsent || src.Kind == sourceResource || src.Kind == sourceModule {
		return out
	}
	rng := src.Range
	if rng.End.Byte > len(f.Src) || rng.Start.Byte >= rng.End.Byte {
		return out
	}
	expr := string(f.Src[rng.Start.Byte:rng.End.Byte])
	if !strings.Contains(expr, "\n") && len(expr) <= maxSourceExpression && secret.CredentialFormat(expr) == "" {
		out.Expression = secret.RedactText(expr, forms)
	}
	return out
}

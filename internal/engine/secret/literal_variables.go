// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// awsAccessKeyIDTag finds a tag key that is an AWS access key ID.
var awsAccessKeyIDTag = regexp.MustCompile(`^(AKIA|ASIA)[0-9A-Z]{16}$`)

// accessKeyTagWarnings returns a warning for each tag of the resources in
// body whose key is an AWS access key ID, as people tag IAM users with the
// keys they hold. An ID alone isn't a secret, but it is worth a look.
func accessKeyTagWarnings(address string, body *hclsyntax.Body) []string {
	var warnings []string
	for _, name := range []string{"tags", "tags_all"} {
		obj, ok := hclfile.AttributeExpr(body, name).(*hclsyntax.ObjectConsExpr)
		if !ok {
			continue
		}
		for _, item := range obj.Items {
			if k := hclfile.ObjectKey(item.KeyExpr); awsAccessKeyIDTag.MatchString(k) {
				warnings = append(warnings, fmt.Sprintf("%s has a tag named after the AWS access key ID %s…%s. An access key ID alone isn't a secret, but check that the tag is meant to be there and that the key's secret isn't kept anywhere near it", address, k[:4], k[len(k)-4:]))
			}
		}
	}
	return warnings
}

// LiteralsToVariables moves each secret value written in the
// resources of the file at path into a new sensitive variable, named after
// the resource and the key the value goes by, other than the variables in
// taken, and rewrites the file. A value inside the JSON document of a
// string is replaced in the string (as ${jsonencode(var.name)}) where
// jsonencode writes it as the string has it; otherwise the whole string
// moves into the variable.
// It returns the variables, with the values, which infraharvest's own plans
// need (see Values), and warnings about tags named after AWS access
// key IDs.
func LiteralsToVariables(path string, taken map[string]bool) ([]Variable, []string, error) {
	file, err := hclfile.Load(path)
	if err != nil {
		return nil, nil, err
	}
	src := file.Src
	used := map[string]bool{}
	for name := range taken {
		if v, ok := strings.CutPrefix(name, "var."); ok {
			used[v] = true
		}
	}
	var edits []edit
	var secrets []Variable
	var warnings []string
	name := func(address, key string) string {
		base := strings.Trim(notInVariableName.ReplaceAllString(address+"_"+strings.ToLower(key), "_"), "_")
		n := base
		for i := 2; used[n]; i++ {
			n = fmt.Sprintf("%s_%d", base, i)
		}
		used[n] = true
		return n
	}
	for _, b := range file.Syntax.Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 {
			continue
		}
		address := b.Labels[0] + "." + b.Labels[1]
		warnings = append(warnings, accessKeyTagWarnings(address, b.Body)...)
		found := secretsInBody(b.Body, "")
		// Group what is inside JSON strings by string: each string is
		// either edited in place or moved whole.
		var order []*hclsyntax.TemplateExpr
		inJSON := map[*hclsyntax.TemplateExpr][]foundSecret{}
		for _, f := range found {
			if f.whole {
				v := name(address, f.key)
				edits = append(edits, edit{f.literal.SrcRange.Start.Byte, f.literal.SrcRange.End.Byte, keepNewline(src, f.literal.SrcRange, "var."+v)})
				secrets = append(secrets, Variable{Variable: v, Address: address, Attribute: f.attribute, key: f.key, value: f.value, Type: cty.String})
				continue
			}
			if _, ok := inJSON[f.literal]; !ok {
				order = append(order, f.literal)
			}
			inJSON[f.literal] = append(inJSON[f.literal], f)
		}
		for _, literal := range order {
			start, end := literal.SrcRange.Start.Byte, literal.SrcRange.End.Byte
			text := string(src[start:end])
			inPlace := strings.HasPrefix(text, `"`)
			for _, f := range inJSON[literal] {
				if !inPlace {
					break
				}
				// The value must be in the text as it is, and jsonencode
				// must write it as the text has it, so that the plan
				// stays the same: jsonencode escapes <, >, & and the
				// Unicode line separators.
				inPlace = !strings.ContainsAny(f.value, "\"\\$%<>&\u2028\u2029") && !strings.ContainsFunc(f.value, unicode.IsControl) &&
					strings.Contains(text, `\"`+f.value+`\"`)
			}
			if !inPlace {
				whole := inJSON[literal][0]
				v := name(address, whole.outerKey)
				edits = append(edits, edit{start, end, keepNewline(src, literal.SrcRange, "var."+v)})
				secrets = append(secrets, Variable{Variable: v, Address: address, Attribute: whole.outer, key: whole.outerKey, value: hclfile.StringLiteral(literal), Type: cty.String})
				continue
			}
			// jsonencode writes the quotes and escapes what the variable
			// is set to, which may need it where the value found doesn't.
			// A value the string repeats is one variable: replacing it
			// replaces every occurrence.
			byValue := map[string]bool{}
			for _, f := range inJSON[literal] {
				if byValue[f.value] {
					continue
				}
				byValue[f.value] = true
				v := name(address, f.key)
				text = strings.ReplaceAll(text, `\"`+f.value+`\"`, `${jsonencode(var.`+v+`)}`)
				secrets = append(secrets, Variable{Variable: v, Address: address, Attribute: f.attribute, key: f.key, value: f.value, Type: cty.String})
			}
			edits = append(edits, edit{start, end, text})
		}
	}
	if len(edits) == 0 {
		return nil, warnings, nil
	}
	out := applyEdits(src, edits)
	if _, diags := hclfile.ParseDiagnostics(path, out); diags.HasErrors() {
		return nil, nil, fmt.Errorf("move secrets out of %s: %w", path, diags)
	}
	return secrets, warnings, fsutil.WriteFile(path, hclwrite.Format(out), 0o644)
}

// edit replaces the bytes of a file from start to end with text.
type edit struct {
	start, end int
	text       string
}

// applyEdits returns src with each of edits, which don't overlap, made:
// one pass over src, not a copy of the rest of it per edit.
func applyEdits(src []byte, edits []edit) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(src)
	for _, e := range edits {
		size += len(e.text) - (e.end - e.start)
	}
	out := make([]byte, 0, size)
	last := 0
	for _, e := range edits {
		out = append(out, src[last:e.start]...)
		out = append(out, e.text...)
		last = e.end
	}
	return append(out, src[last:]...)
}

// keepNewline returns text, followed by a newline if the source range ends
// with one, as a heredoc's does.
func keepNewline(src []byte, rng hcl.Range, text string) string {
	if rng.End.Byte > 0 && rng.End.Byte <= len(src) && src[rng.End.Byte-1] == '\n' {
		return text + "\n"
	}
	return text
}

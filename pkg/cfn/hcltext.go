// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/pkg/cfn/hclname"
)

// The converter writes HCL as text, a piece at a time, and hands each file
// to hclwrite to parse and format (see Convert). Text is the plainest way
// to write template strings, heredocs and comments, which hclwrite's token
// builders don't cover; parsing every file back guarantees the output is
// valid HCL whatever went into it.

// identifierPattern matches the names the converter writes bare: block
// types, labels, argument names and object keys. HCL allows "-" in an
// identifier too, but an object key with one, such as us-east-1, reads
// better quoted.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// traversalPattern matches an attribute path a Mapper may return, such as
// "arn" or "endpoint[0].address".
var traversalPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*|\[[0-9]+\])*$`)

// keywords are the identifiers HCL reads as something other than a name in
// an object key's place.
var keywords = map[string]bool{"true": true, "false": true, "null": true, "for": true, "in": true, "if": true}

// number returns s as an HCL number when it is one, and quoted otherwise.
func number(s string) string {
	if lit, ok := hclname.NumberLiteral(s); ok {
		return lit
	}
	return quote(s)
}

// quote returns s as a quoted HCL string. Template sequences are escaped
// ($${ and %%{), so the string means what it says.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	writeLiteral(&b, s)
	b.WriteByte('"')
	return b.String()
}

// writeLiteral writes s as the literal text of a quoted template.
func writeLiteral(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '$', '%':
			b.WriteByte(c)
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteByte(c)
			}
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
}

// writeHeredocLiteral writes s as the literal text of a heredoc, where only
// template sequences need escaping.
func writeHeredocLiteral(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		b.WriteByte(c)
		if (c == '$' || c == '%') && i+1 < len(s) && s[i+1] == '{' {
			b.WriteByte(c)
		}
	}
}

// heredocDelimiter returns a heredoc delimiter that no line of text is,
// and whether text suits a heredoc at all: several lines, the last ending
// in a newline (a heredoc's value always does), no carriage returns, and
// no control characters a heredoc can't escape.
func heredocDelimiter(text string) (string, bool) {
	if !strings.Contains(strings.TrimSuffix(text, "\n"), "\n") || !strings.HasSuffix(text, "\n") {
		return "", false
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return "", false
		}
	}
	lines := strings.Split(text, "\n")
	for _, d := range []string{"EOT", "EOF", "END_OF_TEXT"} {
		clash := false
		for _, l := range lines {
			if strings.TrimSpace(l) == d {
				clash = true
				break
			}
		}
		if !clash {
			return d, true
		}
	}
	return "", false
}

// objectKey returns k as an object key: bare when it is a plain name, and
// quoted otherwise.
func objectKey(k string) string {
	if identifierPattern.MatchString(k) && !keywords[k] {
		return k
	}
	return quote(k)
}

// listExpr returns elems as a tuple: on one line when it is short and
// simple, and one element a line otherwise.
func listExpr(elems []string) string {
	if len(elems) == 0 {
		return "[]"
	}
	line := "[" + strings.Join(elems, ", ") + "]"
	if len(line) <= 80 && !strings.Contains(line, "\n") {
		return line
	}
	return "[\n" + strings.Join(elems, ",\n") + ",\n]"
}

// objectAttr is a key and value of an object expression.
type objectAttr struct {
	key, value string
}

// objectExpr returns an object expression, one attribute a line; keys are
// as given, so callers quote them with objectKey.
func objectExpr(attrs []objectAttr) string {
	if len(attrs) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, a := range attrs {
		b.WriteString(a.key)
		b.WriteString(" = ")
		b.WriteString(a.value)
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.String()
}

// exprParses reports whether expr is a valid HCL expression, by parsing it
// as an attribute's value.
func exprParses(expr string) bool {
	src := "x = " + expr
	if !strings.HasSuffix(src, "\n") {
		src += "\n"
	}
	_, diags := hclsyntax.ParseConfig([]byte(src), "expr.tf", hcl.InitialPos)
	return !diags.HasErrors()
}

// hclWriter builds a file's or a block's text.
type hclWriter struct {
	b strings.Builder
}

// comment writes text as comment lines.
func (w *hclWriter) comment(text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			w.b.WriteString("#\n")
			continue
		}
		w.b.WriteString("# ")
		w.b.WriteString(line)
		w.b.WriteString("\n")
	}
}

// attr writes name = expr.
func (w *hclWriter) attr(name, expr string) {
	w.b.WriteString(name)
	w.b.WriteString(" = ")
	w.b.WriteString(expr)
	if !strings.HasSuffix(expr, "\n") {
		w.b.WriteString("\n")
	}
}

// open starts a block.
func (w *hclWriter) open(typ string, labels ...string) {
	w.b.WriteString(typ)
	for _, l := range labels {
		w.b.WriteString(" ")
		w.b.WriteString(quote(l))
	}
	w.b.WriteString(" {\n")
}

// close ends a block.
func (w *hclWriter) close() {
	w.b.WriteString("}\n")
}

// blank writes an empty line, between blocks.
func (w *hclWriter) blank() {
	w.b.WriteString("\n")
}

// raw writes text as it is.
func (w *hclWriter) raw(text string) {
	w.b.WriteString(text)
}

func (w *hclWriter) String() string {
	return w.b.String()
}

// snake returns a CloudFormation name in snake_case (see hclname.Snake):
// VPCGatewayAttachment becomes vpc_gateway_attachment.
func snake(s string) string {
	return hclname.Snake(s)
}

// ident returns s as a Terraform name (see hclname.Name): snake_case, and
// never empty or starting with a digit.
func ident(s string) string {
	return hclname.Name(s)
}

// nameSet hands out distinct names in one namespace. A name already given
// out gets a numeric suffix, so two logical IDs that snake_case alike
// (VpcId, VPCId) stay distinct, in template order.
type nameSet struct {
	used map[string]bool
}

func newNameSet() *nameSet {
	return &nameSet{used: map[string]bool{}}
}

func (s *nameSet) claim(base string) string {
	name := base
	for i := 2; s.used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	s.used[name] = true
	return name
}

// sentence returns s as Terraform wants an error message: starting with a
// capital and ending in a full stop or question mark.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	s = string(r)
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") {
		s += "."
	}
	return s
}

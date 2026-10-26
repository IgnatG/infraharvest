// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"regexp"
	"strings"
)

// segment is a piece of a template string: literal text, or an
// expression to interpolate.
type segment struct {
	text   string
	expr   string
	isExpr bool
}

// renderSegments writes segments as an HCL template: a heredoc if heredoc
// allows one and the text suits it, a quoted string otherwise, and the
// expression alone when it is all there is. An expression that is a plain
// string literal, such as one a Resolver gives, becomes text.
func renderSegments(segs []segment, heredoc bool) string {
	// Literals into text, and adjacent text together, so that escaping
	// sees each run of text whole.
	var merged []segment
	for _, s := range segs {
		if s.isExpr {
			if text, ok := plainString(s.expr); ok {
				s = segment{text: text}
			}
		}
		if !s.isExpr && len(merged) > 0 && !merged[len(merged)-1].isExpr {
			merged[len(merged)-1].text += s.text
			continue
		}
		merged = append(merged, s)
	}
	switch {
	case len(merged) == 0:
		return `""`
	case len(merged) == 1 && merged[0].isExpr:
		return merged[0].expr
	}
	var shape strings.Builder // the text with each expression as ${}, to choose the form
	for _, s := range merged {
		if s.isExpr {
			shape.WriteString("${}")
		} else {
			shape.WriteString(s.text)
		}
	}
	d, useHeredoc := heredocDelimiter(shape.String())
	useHeredoc = useHeredoc && heredoc
	literal := writeLiteral
	var b strings.Builder
	if useHeredoc {
		literal = writeHeredocLiteral
		b.WriteString("<<" + d + "\n")
	} else {
		b.WriteByte('"')
	}
	for i, s := range merged {
		if s.isExpr {
			b.WriteString("${" + s.expr + "}")
			continue
		}
		text := s.text
		if i+1 < len(merged) {
			// A $ right before an interpolation would make $${, which HCL
			// reads as a literal ${: write those dollars as an
			// interpolation of their own.
			trimmed := strings.TrimRight(text, "$")
			if dollars := len(text) - len(trimmed); dollars > 0 {
				literal(&b, trimmed)
				b.WriteString(`${"` + strings.Repeat("$", dollars) + `"}`)
				continue
			}
		}
		literal(&b, text)
	}
	if useHeredoc {
		b.WriteString(d + "\n")
	} else {
		b.WriteByte('"')
	}
	return b.String()
}

// dynamicReference matches a CloudFormation dynamic reference, such as
// {{resolve:ssm:/app/db-host}}, which CloudFormation replaces with the
// value it names when it deploys the template.
var dynamicReference = regexp.MustCompile(`\{\{resolve:[^{}]*\}\}`)

// stringValue converts a string literal, whose dynamic references become
// reads of what they name (see dynamicRef). heredoc allows a heredoc.
func (c *converter) stringValue(s, path string, heredoc bool) string {
	segs, ok := c.literalSegments(s, path)
	if !ok {
		return "null"
	}
	return renderSegments(segs, heredoc)
}

// literalSegments splits literal text at its dynamic references. It
// reports false, after an Error diagnostic, when one can't be read: the
// value is then null, never the reference's text, which CloudFormation
// would have replaced.
func (c *converter) literalSegments(s, path string) ([]segment, bool) {
	matches := dynamicReference.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return []segment{{text: s}}, true
	}
	var segs []segment
	last := 0
	for _, m := range matches {
		if m[0] > last {
			segs = append(segs, segment{text: s[last:m[0]]})
		}
		e, ok := c.dynamicRef(s[m[0]:m[1]], path)
		if !ok {
			return nil, false
		}
		segs = append(segs, segment{expr: e, isExpr: true})
		last = m[1]
	}
	if last < len(s) {
		segs = append(segs, segment{text: s[last:]})
	}
	return segs, true
}

// dynamicRef converts a dynamic reference to a data source's value:
//
//   - {{resolve:ssm:name}} and {{resolve:ssm:name:version}} to
//     aws_ssm_parameter's insecure_value (a String or StringList
//     parameter), the name with its version selector;
//   - {{resolve:ssm-secure:name[:version]}} to its value, which is
//     sensitive;
//   - {{resolve:secretsmanager:secret-id:SecretString:json-key:version-stage:version-id}},
//     where all but secret-id may be empty or left off, to
//     aws_secretsmanager_secret_version's secret_string, or the JSON key
//     of it, with the version stage or ID.
//
// One the converter can't read, of another service or malformed, is an
// Error diagnostic.
func (c *converter) dynamicRef(ref, path string) (string, bool) {
	if e, ok := c.dynamic[ref]; ok {
		if c.dynamicSensitive[ref] {
			c.sensitive = true
		}
		return e, true
	}
	fail := func(why string) (string, bool) {
		c.report(Error, path, fmt.Sprintf("the dynamic reference %s %s; it is null instead, not the reference's text", ref, why))
		return "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(ref, "{{resolve:"), "}}")
	service, rest, _ := strings.Cut(inner, ":")
	var block, expr string
	sensitive := false
	switch service {
	case "ssm", "ssm-secure":
		if strings.TrimSpace(rest) == "" {
			return fail("names no parameter")
		}
		label := c.dataNames.claim(ident(lastSegment(strings.TrimRight(rest, "0123456789:"))))
		block = fmt.Sprintf("data \"aws_ssm_parameter\" %s {\n  name = %s\n}\n", quote(label), quote(rest))
		expr = "data.aws_ssm_parameter." + label + ".insecure_value"
		if service == "ssm-secure" {
			expr = "data.aws_ssm_parameter." + label + ".value"
			sensitive = true
		}
	case "secretsmanager":
		id, fields := splitSecretReference(rest)
		if id == "" {
			return fail("names no secret")
		}
		if len(fields) > 4 {
			return fail("has more parts than secret-id:SecretString:json-key:version-stage:version-id")
		}
		for len(fields) < 4 {
			fields = append(fields, "")
		}
		secretString, key, stage, versionID := fields[0], fields[1], fields[2], fields[3]
		switch {
		case secretString != "" && secretString != "SecretString":
			return fail("asks for " + secretString + ", and only SecretString can be read")
		case stage != "" && versionID != "":
			return fail("names both a version stage and a version ID")
		}
		label := c.dataNames.claim(ident(lastSegment(id)))
		var b strings.Builder
		fmt.Fprintf(&b, "data \"aws_secretsmanager_secret_version\" %s {\n  secret_id = %s\n", quote(label), quote(id))
		if stage != "" {
			fmt.Fprintf(&b, "  version_stage = %s\n", quote(stage))
		}
		if versionID != "" {
			fmt.Fprintf(&b, "  version_id = %s\n", quote(versionID))
		}
		b.WriteString("}\n")
		block = b.String()
		expr = "data.aws_secretsmanager_secret_version." + label + ".secret_string"
		if key != "" {
			expr = fmt.Sprintf("jsondecode(%s)[%s]", expr, quote(key))
		}
		sensitive = true
	default:
		return fail("is of " + service + ", which the converter can't read")
	}
	c.dynamic[ref] = expr
	c.dynamicSensitive[ref] = sensitive
	c.dynamicBlocks = append(c.dynamicBlocks, block)
	if sensitive {
		c.sensitive = true
	}
	return expr, true
}

// splitSecretReference splits what follows secretsmanager: in a dynamic
// reference into the secret's ID, which may be an ARN with colons of its
// own, and the fields after it.
func splitSecretReference(rest string) (string, []string) {
	parts := strings.Split(rest, ":")
	n := 1
	// arn:partition:secretsmanager:region:account:secret:name
	if parts[0] == "arn" && len(parts) >= 7 {
		n = 7
	}
	if len(parts) < n {
		return "", nil
	}
	return strings.Join(parts[:n], ":"), parts[n:]
}

// lastSegment returns what follows the last / or : of a name, or "value"
// for none, to name a data source after.
func lastSegment(name string) string {
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "value"
	}
	return name
}

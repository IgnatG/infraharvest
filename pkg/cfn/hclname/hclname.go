// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package hclname holds what the CloudFormation converter (package cfn)
// writes that a program building on its output must write alike: the
// Terraform names of CloudFormation names, numbers as HCL literals, and
// the variables and data sources of SSM parameter types. infraharvest's
// stack intent uses it to name what it carries into a root the way cfn
// does. Like cfn, it imports nothing else of infraharvest's.
package hclname

import (
	"math/big"
	"regexp"
	"strings"
)

// NumberPattern matches the numbers HCL and JSON both read as numbers.
var NumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// reservedVariables are names Terraform doesn't allow for a variable.
var reservedVariables = map[string]bool{
	"source": true, "version": true, "providers": true, "count": true, "for_each": true,
	"lifecycle": true, "depends_on": true, "locals": true,
}

// Snake returns a CloudFormation name in snake_case: VPCGatewayAttachment
// becomes vpc_gateway_attachment, MyS3Bucket my_s3_bucket. A run of
// capitals is one word, whose last capital starts the next word when a
// lower-case letter follows it. Anything but ASCII letters and digits
// becomes an underscore, and runs of underscores become one.
func Snake(s string) string {
	s = strings.NewReplacer("IPv6", "Ipv6", "IPv4", "Ipv4").Replace(s)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			if i > 0 {
				prev := s[i-1]
				nextLower := i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z'
				if isLower(prev) || isDigit(prev) || (prev >= 'A' && prev <= 'Z' && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteByte(c - 'A' + 'a')
		case isLower(c) || isDigit(c):
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return strings.Trim(out, "_")
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Name returns a CloudFormation name, such as a logical ID or a parameter's
// name, as the Terraform name the converter gives it: Snake, and never
// empty or starting with a digit.
func Name(s string) string {
	name := Snake(s)
	if name == "" {
		return "unnamed"
	}
	if isDigit(name[0]) {
		return "_" + name
	}
	return name
}

// VariableName returns a parameter's name as the name of the Terraform
// variable the converter declares for it: Name, with _value added to a
// name Terraform reserves for its own meta-arguments, such as version or
// count, which no variable may have.
func VariableName(s string) string {
	name := Name(s)
	if reservedVariables[name] {
		name += "_value"
	}
	return name
}

// NumberLiteral returns s as an HCL number: as it is when HCL reads it so,
// and otherwise in a canonical form, for a number a template may write in
// a form HCL doesn't read, such as +5, .5, 5. or YAML's 0x1F. It reports
// false for anything else, an infinity or NaN among them, which HCL has no
// literal for.
func NumberLiteral(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if NumberPattern.MatchString(s) {
		return s, true
	}
	lower := strings.ToLower(s)
	if s == "" || strings.Contains(lower, "inf") || strings.Contains(lower, "nan") {
		return "", false
	}
	f, ok := new(big.Float).SetPrec(256).SetString(s)
	if !ok {
		return "", false
	}
	if f.IsInt() {
		return f.Text('f', 0), true
	}
	return f.Text('g', -1), true
}

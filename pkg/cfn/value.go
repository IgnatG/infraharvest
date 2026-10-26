// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"errors"
	"fmt"
	"sort"
)

// Resolver names the Terraform side of a template's references, for a
// caller that knows it better than the converter: one that imported the
// stack's resources, say, and knows the address each logical ID was
// imported at, and the value each parameter had. With a Resolver, a Ref
// or Fn::GetAtt of a resource uses the address Resource gives, and a Ref
// to a parameter or a pseudo parameter the expression Parameter gives; a
// reference either has no answer for is an Error diagnostic and null, so
// that the caller can tell the value isn't complete. Names the Mapper
// guessed are Warnings rather than Info, for the same reason.
type Resolver interface {
	// Resource returns the address of the Terraform resource a logical ID
	// is, such as aws_s3_bucket.logs, and false if there is none.
	Resource(logicalID string) (string, bool)
	// Parameter returns the expression a Ref to a parameter, or to a
	// pseudo parameter such as AWS::StackName, becomes, and false if it has
	// none. A pseudo parameter it has none for becomes what Convert makes
	// of it, such as data.aws_region.current.region, where that needs no
	// stand-in variable.
	Parameter(name string) (string, bool)
}

// Value is one value of a template converted on its own (see
// ConvertValue).
type Value struct {
	// Expr is the HCL expression.
	Expr string
	// Data are the data source blocks Expr reads, such as
	// data "aws_region" "current" {}, as HCL, sorted.
	Data []string
	// Sensitive says Expr reads a NoEcho parameter.
	Sensitive bool
	// Diagnostics are what couldn't be converted: a value with an Error
	// diagnostic has null in its place.
	Diagnostics []Diagnostic
}

// HasErrors reports whether a diagnostic of the value is an Error.
func (v *Value) HasErrors() bool {
	for _, d := range v.Diagnostics {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// ConvertValue converts one value of t, such as an output's Value, into
// an expression, outside a whole configuration: there are no locals, so a
// condition, a mapping or a stand-in variable it would need is an Error
// diagnostic. path is where the value is in the template, for the
// diagnostics, such as "Outputs.VpcId.Value". It returns an error only for
// a nil template, or a value that expands past the converter's budget.
func ConvertValue(t *Template, n Node, path string, opts Options) (*Value, error) {
	if t == nil {
		return nil, errors.New("cfn: no template to convert")
	}
	c := newConverter(t, opts)
	c.single = true
	expr := c.topExpr(n, path, nil)
	if c.tooLarge {
		return nil, fmt.Errorf("cfn: the value at %s converts to more than %d MiB", path, maxOutput>>20)
	}
	v := &Value{Expr: expr, Sensitive: c.sensitive, Diagnostics: c.diags}
	for d := range c.data {
		switch d {
		case dataAZs:
			v.Data = append(v.Data, "data \"aws_availability_zones\" \"available\" {\n  state = \"available\"\n}\n")
		default:
			v.Data = append(v.Data, "data "+quote(d)+" \"current\" {}\n")
		}
	}
	v.Data = append(v.Data, c.dynamicBlocks...)
	sort.Strings(v.Data)
	return v, nil
}

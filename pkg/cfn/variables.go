// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"regexp"
	"strings"
)

// paramType is what a parameter type becomes.
type paramType struct {
	hcl    string // the variable's type
	list   bool   // the variable is a list
	number bool   // the variable, or its elements, are numbers
	// aws is set for the AWS-specific types, such as AWS::EC2::VPC::Id,
	// which Terraform has no equivalent of: the variable is a string, or a
	// list of strings, and its description names the type.
	aws bool
	// ssm is set for AWS::SSM::Parameter::Value<...>: the variable holds the
	// SSM parameter's name, and an aws_ssm_parameter data source reads its
	// value; ssmList when the value is a list, which the data source
	// returns comma-separated.
	ssm     bool
	ssmList bool
	known   bool
}

const ssmValuePrefix = "AWS::SSM::Parameter::Value<"

func parseParamType(t string) paramType {
	switch {
	case t == "String":
		return paramType{hcl: "string", known: true}
	case t == "Number":
		return paramType{hcl: "number", number: true, known: true}
	case t == "List<Number>":
		return paramType{hcl: "list(number)", list: true, number: true, known: true}
	case t == "CommaDelimitedList":
		return paramType{hcl: "list(string)", list: true, known: true}
	case strings.HasPrefix(t, ssmValuePrefix) && strings.HasSuffix(t, ">"):
		inner := strings.TrimSuffix(strings.TrimPrefix(t, ssmValuePrefix), ">")
		return paramType{hcl: "string", ssm: true, ssmList: inner == "CommaDelimitedList" || strings.HasPrefix(inner, "List<"), known: true}
	case strings.HasPrefix(t, "List<AWS::") && strings.HasSuffix(t, ">"):
		return paramType{hcl: "list(string)", list: true, aws: true, known: true}
	case strings.HasPrefix(t, "AWS::"):
		return paramType{hcl: "string", aws: true, known: true}
	}
	return paramType{hcl: "string"}
}

// writeParameters writes a variable for each parameter, after a note on
// the template's rules, which constrain parameters too.
func (c *converter) writeParameters(w *hclWriter) {
	if c.t.Rules != nil {
		names := compactJSON(c.t.Rules)
		if m, ok := c.t.Rules.(Map); ok {
			names = strings.Join(m.Keys(), ", ")
		}
		c.report(Warning, "Rules", fmt.Sprintf("the template's rules (%s) aren't converted: write them as validation blocks on the variables, or as check blocks", names))
		c.flush(w)
		w.blank()
	}
	for _, p := range c.t.Parameters {
		c.writeVariable(w, c.params[p.Name])
		w.blank()
	}
}

func (c *converter) writeVariable(w *hclWriter, pi *paramInfo) {
	p, pt := pi.p, pi.typ
	path := "Parameters." + p.Name
	if !pt.known {
		c.report(Warning, path+".Type", fmt.Sprintf("%q isn't a parameter type the converter knows; the variable is a string", p.Type))
	}
	var body hclWriter
	body.attr("type", pt.hcl)
	desc := strings.TrimSpace(p.Description)
	switch {
	case pt.ssm:
		desc = describe(desc, fmt.Sprintf("The name of the SSM parameter to read the value of: the CloudFormation type was %s.", p.Type))
	case pt.aws:
		desc = describe(desc, fmt.Sprintf("CloudFormation type %s.", p.Type))
	}
	if desc != "" {
		body.attr("description", quote(desc))
	}
	if p.Default != nil {
		if p.NoEcho {
			c.report(Warning, path+".Default", "the default of a NoEcho parameter isn't written, so that the secret stays out of the configuration: set the variable from a secret store")
		} else {
			body.attr("default", c.paramDefault(pi, path+".Default"))
		}
	}
	if p.NoEcho {
		body.attr("sensitive", "true")
	}
	for _, v := range c.validations(pi, path) {
		body.open("validation")
		body.attr("condition", v.condition)
		body.attr("error_message", quote(v.message))
		body.close()
	}
	c.flush(w)
	w.open("variable", pi.name)
	w.raw(body.String())
	w.close()
}

// describe adds what the converter has to say about a variable to the
// parameter's description.
func describe(desc, extra string) string {
	if desc == "" {
		return extra
	}
	return desc + " (" + strings.TrimSuffix(extra, ".") + ")"
}

// paramDefault converts a parameter's default. CloudFormation writes every
// default as a string: a list's as comma-separated values (each trimmed of
// spaces, as CloudFormation trims them), a number's as its digits.
func (c *converter) paramDefault(pi *paramInfo, path string) string {
	pt := pi.typ
	s, isScalar := scalar(pi.p.Default)
	switch {
	case pt.list && isScalar:
		if strings.TrimSpace(s) == "" {
			return "[]"
		}
		var items []string
		for _, item := range strings.Split(s, ",") {
			item = strings.TrimSpace(item)
			if pt.number {
				items = append(items, c.numberLiteral(item, path))
			} else {
				items = append(items, quote(item))
			}
		}
		return listExpr(items)
	case pt.number && isScalar:
		return c.numberLiteral(s, path)
	case isScalar:
		return quote(s)
	}
	return c.expr(pi.p.Default, path, nil)
}

// numberLiteral returns s as a number, or quoted with a warning when it
// isn't one.
func (c *converter) numberLiteral(s, path string) string {
	if numberPattern.MatchString(s) {
		return s
	}
	c.report(Warning, path, fmt.Sprintf("%q isn't a number", s))
	return quote(s)
}

// validation is a variable's validation block.
type validation struct {
	condition, message string
}

// validations converts a parameter's constraints, one validation block
// each, so that a failing value says which constraint it failed. A list
// parameter's constraints apply to each element. ConstraintDescription,
// when the template has one, is the message of every block.
func (c *converter) validations(pi *paramInfo, path string) []validation {
	p, pt := pi.p, pi.typ
	v := "var." + pi.name
	each := func(check func(x string) string) string {
		if pt.list {
			return fmt.Sprintf("alltrue([for v in %s : %s])", v, check("v"))
		}
		return check(v)
	}
	message := func(def string) string {
		if p.ConstraintDescription != "" {
			return sentence(p.ConstraintDescription)
		}
		return def
	}
	var out []validation
	if len(p.AllowedValues) > 0 {
		var values, shown []string
		for i, a := range p.AllowedValues {
			s, ok := scalar(a)
			if !ok {
				c.report(Warning, fmt.Sprintf("%s.AllowedValues[%d]", path, i), "an allowed value that isn't a plain value is left out of the validation")
				c.keep(a)
				continue
			}
			if pt.number {
				values = append(values, c.numberLiteral(s, fmt.Sprintf("%s.AllowedValues[%d]", path, i)))
			} else {
				values = append(values, quote(s))
			}
			shown = append(shown, s)
		}
		if len(values) > 0 {
			allowed := listExpr(values)
			out = append(out, validation{
				each(func(x string) string { return fmt.Sprintf("contains(%s, %s)", allowed, x) }),
				message(fmt.Sprintf("The value of %s must be one of %s.", p.Name, strings.Join(shown, ", "))),
			})
		}
	}
	if p.AllowedPattern != "" {
		// CloudFormation matches the whole value against the pattern.
		pattern := "^(?:" + p.AllowedPattern + ")$"
		if _, err := regexp.Compile(pattern); err != nil {
			c.report(Warning, path+".AllowedPattern", fmt.Sprintf("the pattern %s isn't one Terraform's regex() accepts (RE2 syntax), so it isn't checked: %v", p.AllowedPattern, err))
		} else {
			out = append(out, validation{
				each(func(x string) string { return fmt.Sprintf("can(regex(%s, %s))", quote(pattern), x) }),
				message(fmt.Sprintf("The value of %s must match the pattern %s.", p.Name, p.AllowedPattern)),
			})
		}
	}
	bound := func(n Number, field, check, op, msg string) {
		if n == "" {
			return
		}
		if !numberPattern.MatchString(string(n)) {
			c.report(Warning, path+"."+field, fmt.Sprintf("%s %q isn't a number, so it isn't checked", field, string(n)))
			return
		}
		out = append(out, validation{
			each(func(x string) string { return fmt.Sprintf(check, x) + " " + op + " " + string(n) }),
			message(fmt.Sprintf(msg, p.Name, string(n))),
		})
	}
	bound(p.MinLength, "MinLength", "length(%s)", ">=", "The value of %s must be at least %s characters long.")
	bound(p.MaxLength, "MaxLength", "length(%s)", "<=", "The value of %s must be at most %s characters long.")
	bound(p.MinValue, "MinValue", "%s", ">=", "The value of %s must be at least %s.")
	bound(p.MaxValue, "MaxValue", "%s", "<=", "The value of %s must be at most %s.")
	return out
}

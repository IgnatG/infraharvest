// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/pkg/cfn/hclname"
)

// scope says how to convert the keys of a mapping in a value: as properties
// of a cfnType resource at prop, through the Mapper, or, when the scope is
// nil, as written (as in a policy document or a mapping).
type scope struct {
	cfnType string
	prop    []string
}

// appendPath returns prop with key added, without sharing prop's array.
func appendPath(prop []string, key string) []string {
	return append(append(make([]string, 0, len(prop)+1), prop...), key)
}

// expr converts a value to an HCL expression. path is where the value is
// in the template, for diagnostics. Once the conversion has written more
// than maxOutput bytes, every expression is null and Convert fails: a
// small template whose values refer to one another, such as Fn::Sub
// variables that each repeat the one inside, could otherwise grow the
// output exponentially.
func (c *converter) expr(n Node, path string, sc *scope) string {
	if c.tooLarge {
		return "null"
	}
	return c.spend(c.convert(n, path, sc))
}

// spend counts e towards maxOutput and returns it, or null once the budget
// is spent.
func (c *converter) spend(e string) string {
	c.written += len(e)
	if c.written > maxOutput {
		c.tooLarge = true
		return "null"
	}
	return e
}

// convert is expr without the budget.
func (c *converter) convert(n Node, path string, sc *scope) string {
	switch v := n.(type) {
	case nil, Null:
		return "null"
	case Bool:
		if v {
			return "true"
		}
		return "false"
	case Number:
		return number(string(v))
	case String:
		return c.stringValue(string(v), path, false)
	case List:
		elems := make([]string, len(v))
		optional := false
		for i, e := range v {
			elems[i] = c.expr(e, fmt.Sprintf("%s[%d]", path, i), sc)
			optional = optional || mayBeNoValue(e)
		}
		// CloudFormation drops an element that is AWS::NoValue; Terraform
		// keeps a null, so filter it out.
		if optional {
			return "[for v in " + listExpr(elems) + " : v if v != null]"
		}
		return listExpr(elems)
	case Map:
		if sc != nil {
			return c.propertyObject(v, path, sc)
		}
		attrs := make([]objectAttr, 0, len(v))
		optional := false
		for _, f := range v {
			// CloudFormation drops a key whose value is AWS::NoValue, as in
			// a policy statement's Condition; Terraform keeps a null.
			if isRefTo(f.Value, "AWS::NoValue") {
				continue
			}
			optional = optional || mayBeNoValue(f.Value)
			attrs = append(attrs, objectAttr{objectKey(f.Key), c.expr(f.Value, path+"."+f.Key, nil)})
		}
		if optional {
			return "{ for k, v in " + objectExpr(attrs) + " : k => v if v != null }"
		}
		return objectExpr(attrs)
	case Func:
		return c.function(v, path, sc)
	}
	return "null"
}

// topExpr converts the whole value of an argument or output, where a
// multi-line string reads best as a heredoc.
func (c *converter) topExpr(n Node, path string, sc *scope) string {
	switch v := n.(type) {
	case String:
		if c.tooLarge {
			return "null"
		}
		return c.spend(c.stringValue(string(v), path, true))
	case Func:
		if v.Name == "Fn::Sub" {
			return c.sub(v, path+".Fn::Sub", true)
		}
	}
	return c.expr(n, path, sc)
}

// operand converts a value used as an operand of an operator or a
// conditional, in parentheses when it is an operator expression itself.
func (c *converter) operand(n Node, path string, sc *scope) string {
	e := c.expr(n, path, sc)
	if needsParens(n) {
		return "(" + e + ")"
	}
	return e
}

// needsParens reports whether a value converts to an operator expression
// (==, &&, ||, !, a conditional), which needs parentheses as an operand.
func needsParens(n Node) bool {
	f, ok := n.(Func)
	if !ok {
		return false
	}
	switch f.Name {
	case "Fn::Equals", "Fn::And", "Fn::Or", "Fn::Not", "Fn::If":
		return true
	case "Fn::Sub":
		tmpl, vars, ok := subArgs(f.Arg)
		if !ok {
			return false
		}
		if parts := parseSub(tmpl); len(parts) == 1 && parts[0].isVar {
			if v, ok := vars.Get(parts[0].text); ok {
				return needsParens(v)
			}
		}
	}
	return false
}

// isRefTo reports whether n is Ref to name.
func isRefTo(n Node, name string) bool {
	f, ok := n.(Func)
	if !ok || f.Name != "Ref" {
		return false
	}
	s, ok := f.Arg.(String)
	return ok && string(s) == name
}

// mayBeNoValue reports whether a value may be AWS::NoValue: Ref to it, or
// a condition with it as a branch.
func mayBeNoValue(n Node) bool {
	if isRefTo(n, "AWS::NoValue") {
		return true
	}
	f, ok := n.(Func)
	if !ok || f.Name != "Fn::If" {
		return false
	}
	a, ok := f.Arg.(List)
	return ok && len(a) == 3 && (mayBeNoValue(a[1]) || mayBeNoValue(a[2]))
}

// args returns a function's argument as a list of lo to hi values, or
// reports that it isn't one.
func (c *converter) args(f Func, path string, lo, hi int) (List, bool) {
	l, ok := f.Arg.(List)
	if ok && len(l) >= lo && len(l) <= hi {
		return l, true
	}
	want := strconv.Itoa(lo)
	if hi != lo {
		want += " to " + strconv.Itoa(hi)
	}
	c.report(Error, path, fmt.Sprintf("%s takes a list of %s values; it is null instead", f.Name, want))
	c.keep(f)
	return nil, false
}

// function converts an intrinsic or condition function.
func (c *converter) function(f Func, path string, sc *scope) string {
	path += "." + f.Name
	switch f.Name {
	case "Ref":
		return c.ref(f, path)
	case "Fn::GetAtt":
		return c.getAtt(f, path)
	case "Fn::Sub":
		return c.sub(f, path, false)
	case "Fn::Join":
		a, ok := c.args(f, path, 2, 2)
		if !ok {
			return "null"
		}
		return fmt.Sprintf("join(%s, %s)", c.expr(a[0], path+"[0]", nil), c.expr(a[1], path+"[1]", nil))
	case "Fn::Select":
		a, ok := c.args(f, path, 2, 2)
		if !ok {
			return "null"
		}
		return fmt.Sprintf("element(%s, %s)", c.expr(a[1], path+"[1]", nil), c.numberExpr(a[0], path+"[0]"))
	case "Fn::Split":
		a, ok := c.args(f, path, 2, 2)
		if !ok {
			return "null"
		}
		return fmt.Sprintf("split(%s, %s)", c.expr(a[0], path+"[0]", nil), c.expr(a[1], path+"[1]", nil))
	case "Fn::Base64":
		return "base64encode(" + c.topExpr(f.Arg, path, nil) + ")"
	case "Fn::Cidr":
		return c.cidr(f, path)
	case "Fn::GetAZs":
		return c.getAZs(f, path)
	case "Fn::If":
		a, ok := c.args(f, path, 3, 3)
		if !ok {
			return "null"
		}
		return fmt.Sprintf("%s ? %s : %s", c.conditionRef(a[0], path+"[0]"), c.operand(a[1], path+"[1]", sc), c.operand(a[2], path+"[2]", sc))
	case "Fn::Equals", "Fn::And", "Fn::Or", "Fn::Not", "Condition":
		return c.conditionFunc(f, path)
	case "Fn::FindInMap":
		return c.findInMap(f, path)
	case "Fn::ImportValue":
		return c.importValue(f, path)
	case "Fn::Length":
		return "length(" + c.expr(f.Arg, path, nil) + ")"
	case "Fn::ToJsonString":
		return "jsonencode(" + c.expr(f.Arg, path, nil) + ")"
	case "Fn::Transform":
		c.report(Error, path, "Fn::Transform runs a macro when CloudFormation processes the template, and isn't converted; it is null instead: convert the processed template")
		c.keep(f)
		return "null"
	}
	c.report(Error, path, fmt.Sprintf("%s isn't a function the converter knows; it is null instead", f.Name))
	c.keep(f)
	return "null"
}

// numberExpr converts a value that should be a number, such as Fn::Select's
// index, which a template may write as a string ("0").
func (c *converter) numberExpr(n Node, path string) string {
	if s, ok := scalar(n); ok {
		if lit, ok := hclname.NumberLiteral(s); ok {
			return lit
		}
	}
	return c.operand(n, path, nil)
}

// literalInt returns a value as an int when it is a literal one.
func literalInt(n Node) (int, bool) {
	s, ok := scalar(n)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(s)
	return i, err == nil
}

// ref converts Ref: to a variable for a parameter, to the attribute the
// Mapper names for a resource, and to a data source or a stand-in variable
// for a pseudo parameter.
func (c *converter) ref(f Func, path string) string {
	arg, ok := f.Arg.(String)
	if !ok {
		c.report(Error, path, "Ref takes the name of a parameter, a resource or a pseudo parameter; it is null instead")
		c.keep(f)
		return "null"
	}
	name := string(arg)
	_, isResource := c.resources[name]
	if c.opts.Resolver != nil && !isResource {
		if e, ok := c.opts.Resolver.Parameter(name); ok {
			return e
		}
	}
	if pi, ok := c.params[name]; ok {
		if c.opts.Resolver != nil {
			c.report(Error, path, fmt.Sprintf("Ref to the parameter %s, which has no value here; it is null instead", name))
			return "null"
		}
		return c.paramRef(pi)
	}
	if strings.HasPrefix(name, "AWS::") {
		return c.pseudo(name, path)
	}
	ri, ok := c.resources[name]
	if !ok {
		c.report(Error, path, fmt.Sprintf("Ref to %s, which isn't a parameter or a resource of the template; it is null instead", name))
		return "null"
	}
	address, ok := c.addressOf(ri, path)
	if !ok {
		return "null"
	}
	m, ok := c.mapper.Ref(ri.r.Type)
	if !ok || !traversalPattern.MatchString(m.Name) {
		c.report(Error, path, fmt.Sprintf("the mapper has no Terraform attribute for Ref to a %s; it is null instead", ri.r.Type))
		return "null"
	}
	if m.Guessed {
		c.guess(path, fmt.Sprintf("Ref to %s reads the attribute %s, which the mapper guessed", name, m.Name))
	}
	if m.Sensitive {
		c.sensitive = true
	}
	return address + "." + m.Name
}

// addressOf returns the address references to a resource use: the one the
// Resolver gives, when there is one, or the resource block's (see
// resourceInfo.address). It reports a resource the converter or the
// Resolver has none for.
func (c *converter) addressOf(ri *resourceInfo, path string) (string, bool) {
	if c.opts.Resolver != nil {
		if address, ok := c.opts.Resolver.Resource(ri.r.LogicalID); ok {
			return address, true
		}
		c.report(Error, path, fmt.Sprintf("%s (%s) has no Terraform address here; it is null instead", ri.r.LogicalID, ri.r.Type))
		return "", false
	}
	if ri.skip != "" {
		c.report(Error, path, fmt.Sprintf("%s (%s) isn't converted, so a reference to it is null instead", ri.r.LogicalID, ri.r.Type))
		return "", false
	}
	return ri.address(), true
}

// guess reports a name the mapper guessed: information when the converter
// writes the whole configuration, which a person reviews, and a warning
// for a Resolver's caller, which applies what it gets without review.
func (c *converter) guess(path, msg string) {
	if c.opts.Resolver != nil {
		c.report(Warning, path, msg)
		return
	}
	c.info(path, msg)
}

// paramRef returns a reference to a parameter's variable, or to the data
// source that reads an SSM parameter's value.
func (c *converter) paramRef(pi *paramInfo) string {
	if pi.typ.ssm {
		return hclname.SSMValue(pi.name, pi.typ.ssmList)
	}
	if pi.p.NoEcho {
		c.sensitive = true
	}
	return "var." + pi.name
}

// pseudo converts Ref to a pseudo parameter.
func (c *converter) pseudo(name, path string) string {
	switch name {
	case "AWS::Region":
		c.data[dataRegion] = true
		return "data.aws_region.current.region"
	case "AWS::AccountId":
		c.data[dataCaller] = true
		return "data.aws_caller_identity.current.account_id"
	case "AWS::Partition":
		c.data[dataPartition] = true
		return "data.aws_partition.current.partition"
	case "AWS::URLSuffix":
		c.data[dataPartition] = true
		return "data.aws_partition.current.dns_suffix"
	case "AWS::NoValue":
		return "null"
	case pseudoStackName, pseudoStackID, pseudoNotificationARNs:
		if c.single {
			c.report(Error, path, fmt.Sprintf("%s has no value here; it is null instead", name))
			return "null"
		}
		v := c.standIn[name]
		if !c.pseudoUsed[name] {
			c.pseudoUsed[name] = true
			c.info(path, fmt.Sprintf("%s is the variable %s: Terraform has no stacks", name, v))
		}
		return "var." + v
	}
	c.report(Error, path, fmt.Sprintf("%s isn't a pseudo parameter the converter knows; it is null instead", name))
	return "null"
}

// getAtt converts Fn::GetAtt to the attribute the Mapper names.
func (c *converter) getAtt(f Func, path string) string {
	var id, attr string
	switch a := f.Arg.(type) {
	case List:
		if len(a) == 2 {
			s, _ := a[0].(String)
			id = string(s)
			switch at := a[1].(type) {
			case String:
				attr = string(at)
			case Func:
				c.report(Error, path, "Fn::GetAtt with an attribute name a function computes isn't converted; it is null instead")
				c.keep(f)
				return "null"
			}
		}
	case String:
		id, attr, _ = strings.Cut(string(a), ".")
	}
	if id == "" || attr == "" {
		c.report(Error, path, "Fn::GetAtt takes a logical ID and an attribute name; it is null instead")
		c.keep(f)
		return "null"
	}
	ri, ok := c.resources[id]
	if !ok {
		c.report(Error, path, fmt.Sprintf("Fn::GetAtt of %s, which isn't a resource of the template; it is null instead", id))
		return "null"
	}
	address, ok := c.addressOf(ri, path)
	if !ok {
		return "null"
	}
	m, ok := c.mapper.Attribute(ri.r.Type, attr)
	if !ok || !traversalPattern.MatchString(m.Name) {
		c.report(Error, path, fmt.Sprintf("the mapper has no Terraform attribute for the %s attribute of a %s; it is null instead", attr, ri.r.Type))
		return "null"
	}
	if m.Guessed {
		c.guess(path, fmt.Sprintf("%s.%s is the attribute %s, which the mapper guessed", id, attr, m.Name))
	}
	if m.Sensitive {
		c.sensitive = true
	}
	return address + "." + m.Name
}

// subPart is a piece of an Fn::Sub string: literal text, or the name of a
// variable.
type subPart struct {
	text  string
	isVar bool
}

// parseSub splits an Fn::Sub string into literal text and ${Name}
// variables. ${!Literal} is the literal text ${Literal}, and a ${ with no
// closing brace, or with nothing in the braces, is literal too.
func parseSub(s string) []subPart {
	var parts []subPart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, subPart{text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "${!") {
			lit.WriteString("${")
			i += 3
			continue
		}
		if strings.HasPrefix(s[i:], "${") {
			if end := strings.IndexByte(s[i+2:], '}'); end >= 0 {
				if name := strings.TrimSpace(s[i+2 : i+2+end]); name != "" {
					flush()
					parts = append(parts, subPart{text: name, isVar: true})
					i += 2 + end + 1
					continue
				}
			}
		}
		lit.WriteByte(s[i])
		i++
	}
	flush()
	return parts
}

// subArgs returns the string and variables of Fn::Sub, in its string form
// or its [string, {variables}] form.
func subArgs(arg Node) (string, Map, bool) {
	switch a := arg.(type) {
	case String:
		return string(a), nil, true
	case List:
		if len(a) != 2 {
			return "", nil, false
		}
		s, ok := a[0].(String)
		if !ok {
			return "", nil, false
		}
		switch v := a[1].(type) {
		case Map:
			return string(s), v, true
		case Null:
			return string(s), nil, true
		}
	}
	return "", nil, false
}

// sub converts Fn::Sub to a template string: each ${Name} becomes an
// interpolation of the variable's value, Ref to Name, or, for
// ${Res.Attr}, Fn::GetAtt. A string that is one variable and nothing else
// becomes that variable's expression. heredoc allows a heredoc for a
// multi-line string, where the caller can take one.
func (c *converter) sub(f Func, path string, heredoc bool) string {
	tmpl, vars, ok := subArgs(f.Arg)
	if !ok {
		c.report(Error, path, "Fn::Sub takes a string, or a list of a string and a mapping of variables; it is null instead")
		c.keep(f)
		return "null"
	}
	parts := parseSub(tmpl)
	switch {
	case len(parts) == 0:
		return `""`
	case len(parts) == 1 && parts[0].isVar:
		return c.subVar(parts[0].text, vars, path)
	}
	// Each variable is converted once, however often the string uses it.
	converted := map[string]string{}
	var segs []segment
	for _, p := range parts {
		if !p.isVar {
			lit, ok := c.literalSegments(p.text, path)
			if !ok {
				return "null"
			}
			segs = append(segs, lit...)
			continue
		}
		e, ok := converted[p.text]
		if !ok {
			e = c.subVar(p.text, vars, path)
			converted[p.text] = e
		}
		segs = append(segs, segment{expr: c.spend(e), isExpr: true})
	}
	if c.tooLarge {
		return "null"
	}
	return c.spend(renderSegments(segs, heredoc))
}

// plainString returns the text of a quoted string expression that needs
// no unescaping: no escapes, quotes or template sequences in it.
func plainString(expr string) (string, bool) {
	if len(expr) < 2 || expr[0] != '"' || expr[len(expr)-1] != '"' {
		return "", false
	}
	text := expr[1 : len(expr)-1]
	if strings.ContainsAny(text, "\"\\") || strings.Contains(text, "${") || strings.Contains(text, "%{") {
		return "", false
	}
	return text, true
}

// subVar converts a variable of Fn::Sub.
func (c *converter) subVar(name string, vars Map, path string) string {
	if v, ok := vars.Get(name); ok {
		return c.expr(v, path+"."+name, nil)
	}
	if id, attr, ok := strings.Cut(name, "."); ok {
		return c.getAtt(Func{Name: "Fn::GetAtt", Arg: List{String(id), String(attr)}}, path)
	}
	return c.ref(Func{Name: "Ref", Arg: String(name)}, path)
}

// cidr converts Fn::Cidr [block, count, bits], which divides block into
// count networks with bits host bits each. With a literal block and bit
// count, the new prefix's extra bits are known, and the networks are
// cidrsubnets() of them (or, for many networks, cidrsubnet() in a for
// expression); otherwise the extra bits are computed from the block's
// prefix length, as the IPv4 or IPv6 length less bits.
func (c *converter) cidr(f Func, path string) string {
	a, ok := c.args(f, path, 3, 3)
	if !ok {
		return "null"
	}
	count, countOK := literalInt(a[1])
	bits, bitsOK := literalInt(a[2])
	if block, ok := a[0].(String); ok && bitsOK {
		if _, network, err := net.ParseCIDR(string(block)); err == nil {
			ones, total := network.Mask.Size()
			if newbits := total - bits - ones; newbits > 0 {
				nb := strconv.Itoa(newbits)
				if countOK && count >= 1 && count <= 8 {
					return "cidrsubnets(" + quote(string(block)) + strings.Repeat(", "+nb, count) + ")"
				}
				return fmt.Sprintf("[for i in range(%s) : cidrsubnet(%s, %s, i)]", c.numberExpr(a[1], path+"[1]"), quote(string(block)), nb)
			}
		}
	}
	// The block is written once, bound to b, so that a nested Fn::Cidr
	// doesn't repeat its block at each level.
	return fmt.Sprintf(`[for b in [%s] : [for i in range(%s) : cidrsubnet(b, (strcontains(b, ":") ? 128 : 32) - %s - tonumber(split("/", b)[1]), i)]][0]`,
		c.expr(a[0], path+"[0]", nil), c.numberExpr(a[1], path+"[1]"), c.numberExpr(a[2], path+"[2]"))
}

// getAZs converts Fn::GetAZs to the zones the provider's region has.
func (c *converter) getAZs(f Func, path string) string {
	switch a := f.Arg.(type) {
	case String:
		if a != "" {
			c.report(Warning, path, fmt.Sprintf("Fn::GetAZs lists the zones of %s, and the data source those of the provider's region: give the data source a provider for %s if they differ", a, a))
		}
	case Null, nil:
	default:
		if !isRefTo(a, "AWS::Region") {
			c.report(Warning, path, "Fn::GetAZs's region is an expression, and the data source lists the zones of the provider's region")
		}
	}
	c.data[dataAZs] = true
	return "data.aws_availability_zones.available.names"
}

// findInMap converts Fn::FindInMap to an index into the mapping's local,
// with try() for the default value AWS::LanguageExtensions allows.
func (c *converter) findInMap(f Func, path string) string {
	a, ok := c.args(f, path, 3, 4)
	if !ok {
		return "null"
	}
	name, _ := a[0].(String)
	if c.single {
		c.report(Error, path, "Fn::FindInMap reads a mapping, which isn't converted with one value; it is null instead")
		return "null"
	}
	local, ok := c.mappings[string(name)]
	if !ok {
		c.report(Error, path, fmt.Sprintf("Fn::FindInMap of %s, which isn't a mapping of the template; it is null instead", compactJSON(a[0])))
		return "null"
	}
	key := func(n Node, path string) string {
		if s, ok := scalar(n); ok {
			return quote(s)
		}
		return c.expr(n, path, nil)
	}
	e := fmt.Sprintf("local.%s[%s][%s]", local, key(a[1], path+"[1]"), key(a[2], path+"[2]"))
	if len(a) == 4 {
		if m, ok := a[3].(Map); ok {
			if d, ok := m.Get("DefaultValue"); ok {
				return fmt.Sprintf("try(%s, %s)", e, c.expr(d, path+"[3].DefaultValue", nil))
			}
		}
		c.report(Warning, path+"[3]", "Fn::FindInMap's fourth argument must be {DefaultValue: value}; it is left out")
		c.keep(a[3])
	}
	return e
}

// importValue converts Fn::ImportValue to a stand-in variable, one per
// export name: Terraform has no stack exports.
func (c *converter) importValue(f Func, path string) string {
	label, nameable := importLabel(f.Arg)
	key := compactJSON(f.Arg)
	iv, ok := c.imports[key]
	if !ok {
		base := "import_value"
		if s := ident(label); nameable && s != "unnamed" {
			base = "import_" + s
		}
		iv = &importInfo{name: c.importNames.claim(base), export: label}
		c.imports[key] = iv
		c.importList = append(c.importList, iv)
	}
	c.report(Warning, path, fmt.Sprintf("Fn::ImportValue reads the export %s of another stack, and Terraform has no exports: it is the variable %s, to set from that stack's output or a terraform_remote_state data source", label, iv.name))
	return "var." + iv.name
}

// importLabel returns the export name Fn::ImportValue reads, as written,
// and whether a variable can be named after it: a literal name, or an
// Fn::Sub string such as ${NetworkStack}-VpcId.
func importLabel(n Node) (string, bool) {
	switch v := n.(type) {
	case String:
		return string(v), true
	case Func:
		if v.Name == "Fn::Sub" {
			if tmpl, _, ok := subArgs(v.Arg); ok {
				return tmpl, true
			}
		}
	}
	return compactJSON(n), false
}

// condition converts a condition: a condition function, or, in JSON,
// {"Condition": name}.
func (c *converter) condition(n Node, path string) string {
	switch v := n.(type) {
	case Map:
		if len(v) == 1 && v[0].Key == "Condition" {
			return c.conditionRef(v[0].Value, path+".Condition")
		}
	case Func:
		return c.function(v, path, nil)
	case Bool:
		return c.expr(v, path, nil)
	}
	c.report(Error, path, "a condition must be a condition function, such as Fn::Equals, or {Condition: name}; it is false instead")
	c.keep(n)
	return "false"
}

// conditionOperand converts a condition used as an operand.
func (c *converter) conditionOperand(n Node, path string) string {
	e := c.condition(n, path)
	if needsParens(n) {
		return "(" + e + ")"
	}
	return e
}

// conditionRef converts the name of a condition to its local.
func (c *converter) conditionRef(n Node, path string) string {
	name, ok := n.(String)
	if !ok {
		c.report(Error, path, "a condition is named by a string; it is false instead")
		c.keep(n)
		return "false"
	}
	if c.single {
		c.report(Error, path, fmt.Sprintf("the condition %s isn't converted with one value; it is false instead", name))
		return "false"
	}
	local, ok := c.conditions[string(name)]
	if !ok {
		c.report(Error, path, fmt.Sprintf("there is no condition %s; it is false instead", name))
		return "false"
	}
	return "local." + local
}

// conditionFunc converts the condition functions.
func (c *converter) conditionFunc(f Func, path string) string {
	switch f.Name {
	case "Condition":
		return c.conditionRef(f.Arg, path)
	case "Fn::Equals":
		a, ok := c.args(f, path, 2, 2)
		if !ok {
			return "false"
		}
		// CloudFormation compares strings. A literal compared with a Number
		// parameter, whose variable is a number, is written as a number;
		// any other literal as a string, so that "true" == true doesn't
		// turn false in Terraform.
		numeric := c.isNumberParam(a[0]) || c.isNumberParam(a[1])
		return c.equalsOperand(a[0], path+"[0]", numeric) + " == " + c.equalsOperand(a[1], path+"[1]", numeric)
	case "Fn::And", "Fn::Or":
		a, ok := c.args(f, path, 2, 10)
		if !ok {
			return "false"
		}
		op := " && "
		if f.Name == "Fn::Or" {
			op = " || "
		}
		parts := make([]string, len(a))
		for i, x := range a {
			parts[i] = c.conditionOperand(x, fmt.Sprintf("%s[%d]", path, i))
		}
		return strings.Join(parts, op)
	case "Fn::Not":
		a, ok := c.args(f, path, 1, 1)
		if !ok {
			return "false"
		}
		return "!" + c.conditionOperand(a[0], path+"[0]")
	}
	return "false"
}

// isNumberParam reports whether n is Ref to a Number parameter.
func (c *converter) isNumberParam(n Node) bool {
	f, ok := n.(Func)
	if !ok || f.Name != "Ref" {
		return false
	}
	name, _ := f.Arg.(String)
	pi, ok := c.params[string(name)]
	return ok && pi.typ.number && !pi.typ.list
}

func (c *converter) equalsOperand(n Node, path string, numeric bool) string {
	if s, ok := scalar(n); ok {
		if lit, ok := hclname.NumberLiteral(s); ok && numeric {
			return lit
		}
		return quote(s)
	}
	return c.operand(n, path, nil)
}

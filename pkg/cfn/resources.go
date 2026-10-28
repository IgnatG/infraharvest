// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"strings"
)

// writeResources writes a resource block for each resource, or, for one
// the converter leaves out, a comment that keeps it as written.
func (c *converter) writeResources(w *hclWriter) {
	for _, r := range c.t.Resources {
		c.writeResource(w, c.resources[r.LogicalID])
		w.blank()
	}
}

func (c *converter) writeResource(w *hclWriter, ri *resourceInfo) {
	r := ri.r
	path := "Resources." + r.LogicalID
	if ri.skip != "" {
		c.remark(ri.skipSeverity, path, ri.skip)
		if ri.skipSeverity >= Warning {
			c.keep(resourceNode(r))
		}
		w.comment(fmt.Sprintf("%s (%s)", r.LogicalID, r.Type))
		c.flush(w)
		return
	}
	if ri.guessedType {
		c.report(Warning, path+".Type", fmt.Sprintf("the Terraform type %s is derived from %s: check that the AWS provider has it", ri.tfType, r.Type))
	}
	preventDestroy := c.policies(r, path)
	c.metadata(r, path)
	for _, o := range r.Other {
		c.report(Warning, path+"."+o.Key, fmt.Sprintf("the %s attribute has no Terraform equivalent and isn't converted", o.Key))
		c.keep(o.Value)
	}
	header := c.notes
	c.notes = nil

	var body hclWriter
	c.guessed, c.guessedSeen = nil, map[string]bool{}
	if r.Condition != "" {
		c.writeAttr(&body, "count", c.conditionRef(String(r.Condition), path+".Condition")+" ? 1 : 0", path+".Condition")
	}
	seen := map[string]bool{}
	for _, f := range r.Properties {
		c.property(&body, r.Type, []string{f.Key}, f.Value, path+".Properties."+f.Key, seen)
	}
	var deps []string
	for _, d := range r.DependsOn {
		di, ok := c.resources[d]
		switch {
		case !ok:
			c.report(Error, path+".DependsOn", fmt.Sprintf("DependsOn names %q, which isn't a resource of the template", d))
		case di.skip != "":
			c.report(Warning, path+".DependsOn", fmt.Sprintf("DependsOn names %q, which isn't converted; the dependency is left out", d))
		default:
			deps = append(deps, di.tfType+"."+di.tfName)
		}
	}
	if len(deps) > 0 {
		c.writeAttr(&body, "depends_on", listExpr(deps), path+".DependsOn")
	}
	c.flush(&body)
	if preventDestroy {
		body.open("lifecycle")
		body.attr("prevent_destroy", "true")
		body.close()
	}
	if len(c.guessed) > 0 {
		list := strings.Join(c.guessed, ", ")
		c.info(path+".Properties", "argument names derived from the property names, to check against the provider's schema: "+list)
		header = append(header, "Check these argument names, derived from the property names: "+list+".")
	}

	w.comment(fmt.Sprintf("%s (%s)", r.LogicalID, r.Type))
	for _, n := range header {
		w.comment(n)
	}
	if body.String() == "" {
		w.raw("resource " + quote(ri.tfType) + " " + quote(ri.tfName) + " {}\n")
		return
	}
	w.open("resource", ri.tfType, ri.tfName)
	w.raw(body.String())
	w.close()
}

// resourceNode returns a resource as the template wrote it, for the
// comment that keeps a resource the converter leaves out.
func resourceNode(r *Resource) Map {
	m := Map{{Key: "Type", Value: String(r.Type)}}
	if r.Condition != "" {
		m = append(m, Field{Key: "Condition", Value: String(r.Condition)})
	}
	if len(r.DependsOn) > 0 {
		deps := make(List, len(r.DependsOn))
		for i, d := range r.DependsOn {
			deps[i] = String(d)
		}
		m = append(m, Field{Key: "DependsOn", Value: deps})
	}
	if r.Properties != nil {
		m = append(m, Field{Key: "Properties", Value: r.Properties})
	}
	for _, f := range []Field{
		{Key: "DeletionPolicy", Value: r.DeletionPolicy},
		{Key: "UpdateReplacePolicy", Value: r.UpdateReplacePolicy},
		{Key: "Metadata", Value: r.Metadata},
	} {
		if f.Value != nil {
			m = append(m, f)
		}
	}
	return append(m, r.Other...)
}

// policies notes what DeletionPolicy and UpdateReplacePolicy asked for,
// which Terraform has no settings for, and returns whether to add
// prevent_destroy (see Options.PreventDestroy).
func (c *converter) policies(r *Resource, path string) bool {
	prevent := false
	if r.DeletionPolicy != nil {
		p := text(r.DeletionPolicy)
		switch p {
		case "Delete":
			// What Terraform does too.
		case "Retain", "RetainExceptOnCreate":
			c.remark(Info, path+".DeletionPolicy", fmt.Sprintf("DeletionPolicy %s: CloudFormation keeps the resource when the stack deletes it, and Terraform destroys what leaves the configuration. To keep it, remove it with a removed block whose lifecycle sets destroy = false", p))
			prevent = c.opts.PreventDestroy
		case "Snapshot":
			c.remark(Info, path+".DeletionPolicy", "DeletionPolicy Snapshot: CloudFormation snapshots the resource before it deletes it. Set the resource's own final snapshot arguments, such as skip_final_snapshot and final_snapshot_identifier on aws_db_instance")
		default:
			c.report(Warning, path+".DeletionPolicy", fmt.Sprintf("DeletionPolicy %q isn't one the converter knows, and isn't converted", p))
		}
	}
	if r.UpdateReplacePolicy != nil {
		p := text(r.UpdateReplacePolicy)
		switch p {
		case "Delete":
		case "Retain":
			c.remark(Info, path+".UpdateReplacePolicy", "UpdateReplacePolicy Retain: CloudFormation keeps the old resource when an update replaces it, and Terraform destroys it")
		case "Snapshot":
			c.remark(Info, path+".UpdateReplacePolicy", "UpdateReplacePolicy Snapshot: CloudFormation snapshots the old resource when an update replaces it, and Terraform doesn't")
		default:
			c.report(Warning, path+".UpdateReplacePolicy", fmt.Sprintf("UpdateReplacePolicy %q isn't one the converter knows, and isn't converted", p))
		}
	}
	return prevent
}

// metadata reports a resource's Metadata, which Terraform has no place
// for. cfn-init's configuration is a warning: an instance that ran
// cfn-init depends on it.
func (c *converter) metadata(r *Resource, path string) {
	if r.Metadata == nil {
		return
	}
	m, _ := r.Metadata.(Map)
	if _, ok := m.Get("AWS::CloudFormation::Init"); ok {
		c.report(Warning, path+".Metadata", "the AWS::CloudFormation::Init metadata isn't converted: cfn-init reads it from the stack on the instance. Move what it configures into the user data or a configuration management tool")
	}
	keys := strings.Join(m.Keys(), ", ")
	if keys == "" {
		keys = compactJSON(r.Metadata)
	}
	c.info(path+".Metadata", "the resource's Metadata isn't carried over: "+keys)
}

// property writes a property of a resource or a block, as the Mapper says.
// seen holds the argument names the body already has, which a second
// property mapping to the same name would redefine.
func (c *converter) property(w *hclWriter, cfnType string, prop []string, v Node, path string, seen map[string]bool) {
	// A property set to AWS::NoValue is as if the template didn't set it.
	if isRefTo(v, "AWS::NoValue") {
		return
	}
	key := prop[len(prop)-1]
	p, ok := c.mapper.Property(cfnType, prop)
	if !ok {
		c.report(Error, path, fmt.Sprintf("the mapper has no Terraform argument for %s; it is left out", key))
		c.keep(v)
		return
	}
	if p.Kind == KindOmit {
		c.report(Warning, path, fmt.Sprintf("%s is left out: %s", key, p.Reason))
		c.keep(v)
		return
	}
	name := c.argName(p, prop, path)
	if p.Kind == KindBlock {
		c.block(w, name, cfnType, prop, v, path)
		return
	}
	if seen[name] {
		c.report(Error, path, fmt.Sprintf("%s maps to %s, which another property already set; it is left out", key, name))
		c.keep(v)
		return
	}
	seen[name] = true
	c.writeAttr(w, name, c.propertyValue(v, p, path, &scope{cfnType: cfnType, prop: prop}, true), path)
}

// argName returns the Terraform name of a property, noting a guess.
func (c *converter) argName(p Property, prop []string, path string) string {
	name := p.Name
	if !identifierPattern.MatchString(name) {
		c.report(Error, path, fmt.Sprintf("the mapper's name %q for %s isn't a valid Terraform name; it is %s instead", name, strings.Join(prop, "."), ident(prop[len(prop)-1])))
		name = ident(prop[len(prop)-1])
	}
	if p.Guessed {
		g := fmt.Sprintf("%s (%s)", name, strings.Join(prop, "."))
		if !c.guessedSeen[g] {
			c.guessedSeen[g] = true
			c.guessed = append(c.guessed, g)
		}
	}
	return name
}

// propertyValue converts a property's value as its kind says. top is set
// for the whole value of an argument, where a heredoc may stand.
func (c *converter) propertyValue(v Node, p Property, path string, sc *scope, top bool) string {
	switch p.Kind {
	case KindTags:
		return c.tags(v, path)
	case KindJSON:
		switch v.(type) {
		case Map, List:
			return "jsonencode(" + c.expr(v, path, nil) + ")"
		}
		return c.expr(v, path, nil)
	case KindMap:
		return c.expr(v, path, nil)
	}
	if p.List {
		return c.asList(v, path, sc)
	}
	if top {
		return c.topExpr(v, path, sc)
	}
	return c.expr(v, path, sc)
}

// asList converts a value for an argument that takes a list where
// CloudFormation takes one value (see Property.List): one value becomes a
// list of it, and each branch of Fn::If is converted so, AWS::NoValue to
// null, which leaves the argument unset.
func (c *converter) asList(v Node, path string, sc *scope) string {
	if isRefTo(v, "AWS::NoValue") {
		return "null"
	}
	if f, ok := v.(Func); ok && f.Name == "Fn::If" {
		if a, ok := f.Arg.(List); ok && len(a) == 3 {
			branch := func(n Node, path string) string {
				e := c.asList(n, path, sc)
				if g, ok := n.(Func); ok && g.Name == "Fn::If" {
					return "(" + e + ")"
				}
				return e
			}
			p := path + ".Fn::If"
			return fmt.Sprintf("%s ? %s : %s", c.conditionRef(a[0], p+"[0]"), branch(a[1], p+"[1]"), branch(a[2], p+"[2]"))
		}
	}
	if c.singular(v) {
		return "[" + c.expr(v, path, sc) + "]"
	}
	return c.expr(v, path, sc)
}

// singular reports whether a value is one value rather than a list, for a
// Property with List set.
func (c *converter) singular(v Node) bool {
	switch t := v.(type) {
	case List, Null, nil:
		return false
	case Func:
		switch t.Name {
		case "Fn::Split", "Fn::GetAZs", "Fn::Cidr":
			return false
		case "Ref":
			name, _ := t.Arg.(String)
			if pi, ok := c.params[string(name)]; ok {
				return !pi.typ.list
			}
			return string(name) != pseudoNotificationARNs
		}
	}
	return true
}

// propertyObject converts a mapping whose keys are properties.
func (c *converter) propertyObject(m Map, path string, sc *scope) string {
	var attrs []objectAttr
	seen := map[string]bool{}
	for _, f := range m {
		if isRefTo(f.Value, "AWS::NoValue") {
			continue
		}
		prop := appendPath(sc.prop, f.Key)
		fpath := path + "." + f.Key
		p, ok := c.mapper.Property(sc.cfnType, prop)
		if !ok {
			c.report(Error, fpath, fmt.Sprintf("the mapper has no Terraform argument for %s; it is left out", f.Key))
			c.keep(f.Value)
			continue
		}
		if p.Kind == KindOmit {
			c.report(Warning, fpath, fmt.Sprintf("%s is left out: %s", f.Key, p.Reason))
			c.keep(f.Value)
			continue
		}
		name := c.argName(p, prop, fpath)
		if seen[name] {
			c.report(Error, fpath, fmt.Sprintf("%s maps to %s, which another property already set; it is left out", f.Key, name))
			c.keep(f.Value)
			continue
		}
		seen[name] = true
		attrs = append(attrs, objectAttr{name, c.propertyValue(f.Value, p, fpath, &scope{cfnType: sc.cfnType, prop: prop}, false)})
	}
	return objectExpr(attrs)
}

// tags converts a list of {Key, Value} mappings to a map. A list whose
// elements are functions, such as Fn::If, becomes a for expression that
// skips the elements that turn out null (AWS::NoValue).
func (c *converter) tags(v Node, path string) string {
	switch t := v.(type) {
	case Map:
		return c.expr(t, path, nil)
	case List:
		literal := true
		for _, el := range t {
			m, ok := el.(Map)
			_, hasKey := m.Get("Key")
			_, hasValue := m.Get("Value")
			if !ok || len(m) != 2 || !hasKey || !hasValue {
				literal = false
				break
			}
		}
		if !literal {
			return fmt.Sprintf("{ for tag in %s : tag.Key => tag.Value if tag != null }", c.expr(t, path, nil))
		}
		attrs := make([]objectAttr, 0, len(t))
		for i, el := range t {
			m := el.(Map)
			k, _ := m.Get("Key")
			val, _ := m.Get("Value")
			ep := fmt.Sprintf("%s[%d]", path, i)
			key := "(" + c.expr(k, ep+".Key", nil) + ")"
			if s, ok := k.(String); ok {
				key = objectKey(string(s))
			}
			attrs = append(attrs, objectAttr{key, c.expr(val, ep+".Value", nil)})
		}
		return objectExpr(attrs)
	}
	c.report(Warning, path, "the tags are an expression; they are written as a map of its Key and Value pairs, which needs checking")
	return fmt.Sprintf("{ for tag in %s : tag.Key => tag.Value }", c.expr(v, path, nil))
}

// block writes a property that Terraform takes as nested blocks: a block
// for a mapping, a block per mapping of a list, and a dynamic block for
// Fn::If with AWS::NoValue as a branch. Anything else is written as an
// argument, with a warning.
func (c *converter) block(w *hclWriter, name, cfnType string, prop []string, v Node, path string) {
	switch t := v.(type) {
	case Map:
		c.blockBody(w, name, cfnType, prop, t, path)
		return
	case List:
		for i, el := range t {
			ep := fmt.Sprintf("%s[%d]", path, i)
			switch e := el.(type) {
			case Map:
				c.blockBody(w, name, cfnType, prop, e, ep)
			case Func:
				if !c.dynamicBlock(w, name, cfnType, prop, e, ep) {
					c.blockArgument(w, name, cfnType, prop, e, ep)
				}
			default:
				c.report(Error, ep, fmt.Sprintf("%s is a block in Terraform, and this element isn't a mapping; it is left out", name))
				c.keep(el)
			}
		}
		return
	case Func:
		if c.dynamicBlock(w, name, cfnType, prop, t, path) {
			return
		}
	}
	c.blockArgument(w, name, cfnType, prop, v, path)
}

func (c *converter) blockBody(w *hclWriter, name, cfnType string, prop []string, m Map, path string) {
	c.flush(w)
	w.open(name)
	seen := map[string]bool{}
	for _, f := range m {
		c.property(w, cfnType, appendPath(prop, f.Key), f.Value, path+"."+f.Key, seen)
	}
	c.flush(w)
	w.close()
}

// dynamicBlock writes Fn::If [cond, {...}, AWS::NoValue], or the other way
// round, as a dynamic block that exists when the condition says so.
func (c *converter) dynamicBlock(w *hclWriter, name, cfnType string, prop []string, f Func, path string) bool {
	if f.Name != "Fn::If" {
		return false
	}
	a, ok := f.Arg.(List)
	if !ok || len(a) != 3 {
		return false
	}
	var content Map
	negate := false
	switch {
	case isRefTo(a[2], "AWS::NoValue"):
		content, ok = a[1].(Map)
	case isRefTo(a[1], "AWS::NoValue"):
		content, ok = a[2].(Map)
		negate = true
	default:
		return false
	}
	if !ok {
		return false
	}
	cond := c.conditionRef(a[0], path+".Fn::If[0]")
	if negate {
		cond = "!" + cond
	}
	c.flush(w)
	w.open("dynamic", name)
	c.writeAttr(w, "for_each", cond+" ? [1] : []", path+".Fn::If")
	w.open("content")
	seen := map[string]bool{}
	for _, fl := range content {
		c.property(w, cfnType, appendPath(prop, fl.Key), fl.Value, path+".Fn::If."+fl.Key, seen)
	}
	c.flush(w)
	w.close()
	w.close()
	return true
}

// blockArgument writes a block's value as an argument, which is what the
// converter can do when the value is an expression.
func (c *converter) blockArgument(w *hclWriter, name, cfnType string, prop []string, v Node, path string) {
	c.report(Warning, path, fmt.Sprintf("%s is a block in Terraform, and the value is an expression; it is written as an argument, which the provider may not accept", name))
	c.writeAttr(w, name, c.expr(v, path, &scope{cfnType: cfnType, prop: prop}), path)
}

// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"strconv"
)

// maxValueDepth bounds how deeply Convert and ConvertValue follow a value,
// and maxValues how many values they follow, so that a Template built by
// hand, rather than by Parse, can't exhaust the stack or loop forever: a
// List can contain itself, and lists that each hold the one before twice
// make a value too large to walk. The converter's recursion follows the
// values, so they are checked before it starts.
//
// Both are above anything Parse returns. One level of YAML can make two
// levels of values (a tagged mapping that is a function, such as
// !Base64 {Fn::Sub: ...}, is a Func of a Func), and a string, number,
// boolean or null nests nothing, so it adds no level. Each value of a JSON
// template takes at least a byte of it, and each node of a YAML template,
// of which maxNodes allows far fewer, makes at most four values
// (!GetAtt Res.Attr is a Func of a List of two Strings).
const (
	maxValueDepth = 2 * (maxDepth + 1)
	maxValues     = maxInput
)

// checkTemplate returns an error when t has a nil entry, which Convert
// would dereference, or values beyond maxValueDepth or maxValues.
func checkTemplate(t *Template) error {
	if err := checkEntries(t); err != nil {
		return err
	}
	w := &valueWalk{budget: maxValues}
	for _, p := range t.Parameters {
		path := "Parameters." + p.Name
		w.check(path+".Default", p.Default)
		for i, v := range p.AllowedValues {
			w.check(path+".AllowedValues["+strconv.Itoa(i)+"]", v)
		}
	}
	for _, m := range t.Mappings {
		w.check("Mappings."+m.Name, m.Value)
	}
	for _, cd := range t.Conditions {
		w.check("Conditions."+cd.Name, cd.Expr)
	}
	for _, r := range t.Resources {
		path := "Resources." + r.LogicalID
		w.check(path+".Properties", r.Properties)
		w.check(path+".DeletionPolicy", r.DeletionPolicy)
		w.check(path+".UpdateReplacePolicy", r.UpdateReplacePolicy)
		w.check(path+".Metadata", r.Metadata)
		for _, f := range r.Other {
			w.check(path+"."+f.Key, f.Value)
		}
	}
	for _, o := range t.Outputs {
		path := "Outputs." + o.Name
		w.check(path+".Value", o.Value)
		w.check(path+".Export", o.Export)
	}
	w.check("Metadata", t.Metadata)
	w.check("Rules", t.Rules)
	for _, f := range t.Loops {
		w.check(f.Key, f.Value)
	}
	for _, f := range t.Other {
		w.check(f.Key, f.Value)
	}
	return w.err
}

// checkValue returns an error when n, at path (which may be empty), goes
// beyond maxValueDepth or maxValues.
func checkValue(n Node, path string) error {
	w := &valueWalk{budget: maxValues}
	w.check(path, n)
	return w.err
}

// checkEntries returns an error when a section of t has a nil entry.
func checkEntries(t *Template) error {
	nilEntry := func(section string, i int) error {
		return fmt.Errorf("cfn: the template's %s[%d] is nil", section, i)
	}
	for i, p := range t.Parameters {
		if p == nil {
			return nilEntry("Parameters", i)
		}
	}
	for i, m := range t.Mappings {
		if m == nil {
			return nilEntry("Mappings", i)
		}
	}
	for i, cd := range t.Conditions {
		if cd == nil {
			return nilEntry("Conditions", i)
		}
	}
	for i, r := range t.Resources {
		if r == nil {
			return nilEntry("Resources", i)
		}
	}
	for i, o := range t.Outputs {
		if o == nil {
			return nilEntry("Outputs", i)
		}
	}
	return nil
}

// valueWalk checks values against the limits, keeping the first error.
// budget counts down the values it may still visit, across all the values
// it checks.
type valueWalk struct {
	budget int
	err    error
}

func (w *valueWalk) check(path string, n Node) {
	if w.err != nil || n == nil {
		return
	}
	where := "the value"
	if path != "" {
		where += " at " + path
	}
	switch {
	case w.walk(n, 0):
	case w.budget < 0:
		// A value counts each time it is repeated, as the converter
		// converts it each time.
		w.err = fmt.Errorf("cfn: %s makes the template hold more than %d values, counting each repeat of a value", where, maxValues)
	default:
		w.err = fmt.Errorf("cfn: %s nests values more than %d deep, or contains itself", where, maxValueDepth)
	}
}

// walk reports whether n, depth levels down, stays within the limits.
func (w *valueWalk) walk(n Node, depth int) bool {
	w.budget--
	if w.budget < 0 {
		return false
	}
	switch v := n.(type) {
	case List:
		if depth >= maxValueDepth {
			return false
		}
		for _, e := range v {
			if !w.walk(e, depth+1) {
				return false
			}
		}
	case Map:
		if depth >= maxValueDepth {
			return false
		}
		for _, f := range v {
			if !w.walk(f.Value, depth+1) {
				return false
			}
		}
	case Func:
		if depth >= maxValueDepth {
			return false
		}
		return w.walk(v.Arg, depth+1)
	}
	return true
}

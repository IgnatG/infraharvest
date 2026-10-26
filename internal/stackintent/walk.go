// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package stackintent

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// directRef returns the parameter a value is Ref to, when it is just that.
func directRef(t *cfn.Template, n cfn.Node) (string, bool) {
	f, ok := n.(cfn.Func)
	if !ok || f.Name != "Ref" {
		return "", false
	}
	name, ok := f.Arg.(cfn.String)
	if !ok || t.Parameter(string(name)) == nil {
		return "", false
	}
	return string(name), true
}

// references returns the names a value refers to: by Ref, by Fn::GetAtt
// (its logical ID), and by the variables of Fn::Sub that its own mapping
// of variables doesn't set.
func references(n cfn.Node) []string {
	var names []string
	var walk func(cfn.Node)
	walk = func(n cfn.Node) {
		switch v := n.(type) {
		case cfn.List:
			for _, e := range v {
				walk(e)
			}
		case cfn.Map:
			for _, f := range v {
				walk(f.Value)
			}
		case cfn.Func:
			switch v.Name {
			case "Ref":
				if s, ok := v.Arg.(cfn.String); ok {
					names = append(names, string(s))
				}
				return
			case "Fn::GetAtt":
				switch a := v.Arg.(type) {
				case cfn.List:
					if len(a) > 0 {
						if s, ok := a[0].(cfn.String); ok {
							names = append(names, string(s))
						}
					}
				case cfn.String:
					id, _, _ := strings.Cut(string(a), ".")
					names = append(names, id)
				}
				return
			case "Fn::Sub":
				tmpl, vars := subArgs(v.Arg)
				for _, name := range subVariables(tmpl) {
					if _, ok := vars.Get(name); ok {
						continue
					}
					id, _, _ := strings.Cut(name, ".")
					names = append(names, id)
				}
				for _, f := range vars {
					walk(f.Value)
				}
				return
			}
			walk(v.Arg)
		}
	}
	walk(n)
	return names
}

// referencedParameters returns the template's parameters a value refers
// to, sorted.
func referencedParameters(t *cfn.Template, n cfn.Node) []string {
	seen := map[string]bool{}
	var params []string
	for _, name := range references(n) {
		if t.Parameter(name) != nil && !seen[name] {
			seen[name] = true
			params = append(params, name)
		}
	}
	sort.Strings(params)
	return params
}

// refersToResource reports whether a value refers to a resource of the
// template.
func refersToResource(t *cfn.Template, n cfn.Node) bool {
	for _, name := range references(n) {
		if t.Resource(name) != nil {
			return true
		}
	}
	return false
}

// subArgs returns the string and variables of Fn::Sub.
func subArgs(arg cfn.Node) (string, cfn.Map) {
	switch a := arg.(type) {
	case cfn.String:
		return string(a), nil
	case cfn.List:
		if len(a) == 2 {
			s, _ := a[0].(cfn.String)
			m, _ := a[1].(cfn.Map)
			return string(s), m
		}
	}
	return "", nil
}

// subVariables returns the ${Name} variables of an Fn::Sub string, leaving
// out the ${!Literal} ones.
func subVariables(s string) []string {
	var names []string
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			return names
		}
		s = s[i+2:]
		if strings.HasPrefix(s, "!") {
			continue
		}
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return names
		}
		if name := strings.TrimSpace(s[:end]); name != "" {
			names = append(names, name)
		}
		s = s[end+1:]
	}
}

// writeJSON writes a template value as one line of JSON.
func writeJSON(b *bytes.Buffer, n cfn.Node) error {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(n); err != nil {
		return err
	}
	b.Truncate(b.Len() - 1) // the newline Encode adds
	return nil
}

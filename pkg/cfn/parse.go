// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxDepth bounds how deeply a template may nest values, and maxNodes how
// many values it may hold once YAML aliases are expanded, so that a hostile
// template can't exhaust the stack or memory.
const (
	maxDepth = 512
	maxNodes = 1 << 20
)

// Parse reads a CloudFormation template in JSON or YAML, including YAML's
// short-form function tags (!Ref, !Sub, !GetAtt and the rest). A template
// whose first character is "{" is read as JSON, and as YAML only if that
// fails; anything else is read as YAML.
//
// Parse checks the template's structure, not its meaning: it fails when a
// section or an entry isn't the kind of value CloudFormation requires (the
// Resources section must be a mapping, a resource must have a Type), and
// leaves references, function arguments and types to Convert, which
// reports problems with them as diagnostics.
func Parse(r io.Reader) (*Template, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read the template: %w", err)
	}
	root, err := parseDocument(src)
	if err != nil {
		return nil, err
	}
	return build(root)
}

// parseDocument parses src as JSON or YAML.
func parseDocument(src []byte) (Node, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimLeft(src, " \t\r\n")
	if len(trimmed) == 0 {
		return nil, errors.New("the template is empty")
	}
	if trimmed[0] == '{' {
		n, jsonErr := parseJSON(trimmed)
		if jsonErr == nil {
			return n, nil
		}
		// A YAML flow mapping starts with "{" too; but JSON followed by
		// more isn't one.
		if n, err := parseYAML(src); err == nil && !errors.Is(jsonErr, errTrailingJSON) {
			return n, nil
		}
		return nil, jsonErr
	}
	return parseYAML(src)
}

// errTrailingJSON is the error for a JSON template with more after it.
var errTrailingJSON = errors.New("parse the JSON template: there is more after the template's closing brace")

func parseJSON(src []byte) (Node, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	n, err := readJSON(dec, 0)
	if err != nil {
		return nil, fmt.Errorf("parse the JSON template: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errTrailingJSON
	}
	return n, nil
}

// readJSON reads the next value from dec. It reads tokens rather than
// decoding into a Go map so that objects keep their key order.
func readJSON(dec *json.Decoder, depth int) (Node, error) {
	if depth > maxDepth {
		return nil, errors.New("the template nests values too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var m Map
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("an object key is %v, not a string", keyTok)
				}
				v, err := readJSON(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m = m.set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return mapOrFunc(m), nil
		case '[':
			l := List{}
			for dec.More() {
				v, err := readJSON(dec, depth+1)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return l, nil
		}
		return nil, fmt.Errorf("unexpected %q", t.String())
	case string:
		return String(t), nil
	case json.Number:
		return Number(t.String()), nil
	case bool:
		return Bool(t), nil
	case nil:
		return Null{}, nil
	}
	return nil, fmt.Errorf("unexpected %v", tok)
}

func parseYAML(src []byte) (Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parse the YAML template: %w", err)
	}
	if doc.Kind == 0 {
		return nil, errors.New("the template is empty")
	}
	y := &yamlReader{budget: maxNodes}
	n, err := y.read(&doc, 0)
	if err != nil {
		return nil, fmt.Errorf("parse the YAML template: %w", err)
	}
	return n, nil
}

// yamlReader turns a YAML node tree into Nodes. budget counts down the
// values it may still produce, which bounds alias expansion.
type yamlReader struct {
	budget int
}

func (y *yamlReader) read(n *yaml.Node, depth int) (Node, error) {
	y.budget--
	if y.budget < 0 {
		return nil, errors.New("the template holds too many values once its aliases are expanded")
	}
	if depth > maxDepth {
		return nil, errors.New("the template nests values too deeply")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return Null{}, nil
		}
		return y.read(n.Content[0], depth)
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("line %d: an alias with no anchor", n.Line)
		}
		return y.read(n.Alias, depth+1)
	case yaml.ScalarNode:
		if name, ok := functionTag(n.Tag); ok {
			return shortForm(name, String(n.Value)), nil
		}
		return yamlScalar(n), nil
	case yaml.SequenceNode:
		l := make(List, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := y.read(c, depth+1)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		if name, ok := functionTag(n.Tag); ok {
			return Func{Name: name, Arg: l}, nil
		}
		return l, nil
	case yaml.MappingNode:
		m, err := y.readMapping(n, depth)
		if err != nil {
			return nil, err
		}
		if name, ok := functionTag(n.Tag); ok {
			return Func{Name: name, Arg: mapOrFunc(m)}, nil
		}
		return mapOrFunc(m), nil
	}
	return nil, fmt.Errorf("line %d: unexpected YAML node", n.Line)
}

// readMapping reads a YAML mapping, applying merge keys (<<: *anchor): the
// merged mappings' keys come after the mapping's own and never replace
// them, whatever the order they are written in.
func (y *yamlReader) readMapping(n *yaml.Node, depth int) (Map, error) {
	var m, merged Map
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind == yaml.AliasNode && k.Alias != nil {
			k = k.Alias
		}
		if k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: a mapping key must be a plain value", k.Line)
		}
		value, err := y.read(v, depth+1)
		if err != nil {
			return nil, err
		}
		if k.Value == "<<" && k.ShortTag() == "!!merge" {
			sources := List{value}
			if l, ok := value.(List); ok {
				sources = l
			}
			for _, s := range sources {
				sm, ok := s.(Map)
				if !ok {
					return nil, fmt.Errorf("line %d: a merge key must refer to mappings", k.Line)
				}
				for _, f := range sm {
					if _, ok := merged.Get(f.Key); !ok {
						merged = append(merged, f)
					}
				}
			}
			continue
		}
		m = m.set(k.Value, value)
	}
	for _, f := range merged {
		if _, ok := m.Get(f.Key); !ok {
			m = append(m, f)
		}
	}
	return m, nil
}

// functionTag returns the long name of a YAML short-form tag, such as
// "Fn::Sub" for !Sub, and whether tag is one. Standard tags (!!str) and the
// non-specific tag (!) aren't.
func functionTag(tag string) (string, bool) {
	if len(tag) < 2 || tag[0] != '!' || tag[1] == '!' || strings.ContainsAny(tag, "<>:/") {
		return "", false
	}
	name := tag[1:]
	if name == "Ref" || name == "Condition" {
		return name, true
	}
	return "Fn::" + name, true
}

// shortForm returns the function a short-form tag on a plain value makes.
// !GetAtt Res.Attr names the resource before the first dot and the
// attribute after it, which may hold dots of its own (Endpoint.Address).
func shortForm(name string, arg String) Node {
	if name == "Fn::GetAtt" {
		if res, attr, ok := strings.Cut(string(arg), "."); ok {
			return Func{Name: name, Arg: List{String(res), String(attr)}}
		}
	}
	return Func{Name: name, Arg: arg}
}

// yamlScalar returns an untagged YAML scalar as the node its resolved tag
// makes it. Timestamps, such as the 2010-09-09 of AWSTemplateFormatVersion
// or an IAM policy's Version, stay strings, as CloudFormation reads them.
func yamlScalar(n *yaml.Node) Node {
	switch n.ShortTag() {
	case "!!null":
		return Null{}
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err == nil {
			return Bool(b)
		}
	case "!!int", "!!float":
		return Number(n.Value)
	}
	return String(n.Value)
}

// build turns a parsed document into a Template.
func build(root Node) (*Template, error) {
	top, ok := root.(Map)
	if !ok {
		return nil, errors.New("a template is a mapping of sections, such as Resources")
	}
	t := &Template{}
	haveResources := false
	for _, f := range top {
		var err error
		switch f.Key {
		case "AWSTemplateFormatVersion":
			t.FormatVersion = text(f.Value)
		case "Description":
			t.Description = text(f.Value)
		case "Transform":
			t.Transform, err = transformNames(f.Value)
		case "Metadata":
			t.Metadata = f.Value
		case "Rules":
			t.Rules = f.Value
		case "Parameters":
			err = t.buildParameters(f.Value)
		case "Mappings":
			err = t.buildMappings(f.Value)
		case "Conditions":
			err = t.buildConditions(f.Value)
		case "Resources":
			haveResources = true
			err = t.buildResources(f.Value)
		case "Outputs":
			err = t.buildOutputs(f.Value)
		default:
			t.Other = append(t.Other, f)
		}
		if err != nil {
			return nil, err
		}
	}
	if !haveResources {
		return nil, errors.New("the template has no Resources section")
	}
	return t, nil
}

// text returns a scalar's text, and anything else as JSON.
func text(n Node) string {
	if s, ok := scalar(n); ok {
		return s
	}
	if _, ok := n.(Null); ok || n == nil {
		return ""
	}
	return compactJSON(n)
}

// section returns a section's entries; a null section has none.
func section(name string, n Node) (Map, error) {
	switch v := n.(type) {
	case Map:
		return v, nil
	case Null, nil:
		return nil, nil
	}
	return nil, fmt.Errorf("%s must be a mapping", name)
}

// isLoop reports whether a section's key is an Fn::ForEach loop.
func isLoop(key string) bool {
	return strings.HasPrefix(key, "Fn::ForEach::")
}

func transformNames(n Node) ([]string, error) {
	var items List
	switch v := n.(type) {
	case List:
		items = v
	default:
		items = List{v}
	}
	var names []string
	for _, item := range items {
		switch v := item.(type) {
		case String:
			names = append(names, string(v))
		case Map:
			// A macro with parameters: {Name: ..., Parameters: ...}.
			name, _ := v.Get("Name")
			s, ok := name.(String)
			if !ok {
				return nil, errors.New("a transform with parameters must have a Name")
			}
			names = append(names, string(s))
		default:
			return nil, errors.New("the Transform section must name a macro, or list macros")
		}
	}
	return names, nil
}

func (t *Template) buildParameters(n Node) error {
	entries, err := section("Parameters", n)
	if err != nil {
		return err
	}
	for _, f := range entries {
		path := "Parameters." + f.Key
		m, ok := f.Value.(Map)
		if !ok {
			return fmt.Errorf("%s must be a mapping", path)
		}
		p := &Parameter{Name: f.Key}
		for _, a := range m {
			switch a.Key {
			case "Type":
				p.Type = text(a.Value)
			case "Description":
				p.Description = text(a.Value)
			case "Default":
				p.Default = a.Value
			case "AllowedValues":
				l, ok := a.Value.(List)
				if !ok {
					return fmt.Errorf("%s.AllowedValues must be a list", path)
				}
				p.AllowedValues = l
			case "AllowedPattern":
				p.AllowedPattern = text(a.Value)
			case "MinLength":
				p.MinLength = Number(text(a.Value))
			case "MaxLength":
				p.MaxLength = Number(text(a.Value))
			case "MinValue":
				p.MinValue = Number(text(a.Value))
			case "MaxValue":
				p.MaxValue = Number(text(a.Value))
			case "ConstraintDescription":
				p.ConstraintDescription = text(a.Value)
			case "NoEcho":
				p.NoEcho = strings.EqualFold(text(a.Value), "true")
			}
		}
		t.Parameters = append(t.Parameters, p)
	}
	return nil
}

func (t *Template) buildMappings(n Node) error {
	entries, err := section("Mappings", n)
	if err != nil {
		return err
	}
	for _, f := range entries {
		m, ok := f.Value.(Map)
		if !ok {
			return fmt.Errorf("the mapping %s must be a mapping of keys", f.Key)
		}
		t.Mappings = append(t.Mappings, &Mapping{Name: f.Key, Value: m})
	}
	return nil
}

func (t *Template) buildConditions(n Node) error {
	entries, err := section("Conditions", n)
	if err != nil {
		return err
	}
	for _, f := range entries {
		if isLoop(f.Key) {
			t.Loops = append(t.Loops, Field{Key: "Conditions." + f.Key, Value: f.Value})
			continue
		}
		t.Conditions = append(t.Conditions, &Condition{Name: f.Key, Expr: f.Value})
	}
	return nil
}

func (t *Template) buildResources(n Node) error {
	entries, err := section("Resources", n)
	if err != nil {
		return err
	}
	for _, f := range entries {
		path := "Resources." + f.Key
		if isLoop(f.Key) {
			t.Loops = append(t.Loops, Field{Key: path, Value: f.Value})
			continue
		}
		m, ok := f.Value.(Map)
		if !ok {
			return fmt.Errorf("%s must be a mapping", path)
		}
		r := &Resource{LogicalID: f.Key}
		for _, a := range m {
			switch a.Key {
			case "Type":
				s, ok := a.Value.(String)
				if !ok {
					return fmt.Errorf("%s.Type must be a string", path)
				}
				r.Type = string(s)
			case "Properties":
				props, err := section(path+".Properties", a.Value)
				if err != nil {
					return err
				}
				r.Properties = props
			case "DependsOn":
				switch v := a.Value.(type) {
				case String:
					r.DependsOn = []string{string(v)}
				case List:
					for _, d := range v {
						s, ok := d.(String)
						if !ok {
							return fmt.Errorf("%s.DependsOn must list logical IDs", path)
						}
						r.DependsOn = append(r.DependsOn, string(s))
					}
				default:
					return fmt.Errorf("%s.DependsOn must be a logical ID or a list of them", path)
				}
			case "Condition":
				r.Condition = text(a.Value)
			case "DeletionPolicy":
				r.DeletionPolicy = a.Value
			case "UpdateReplacePolicy":
				r.UpdateReplacePolicy = a.Value
			case "Metadata":
				r.Metadata = a.Value
			default:
				r.Other = append(r.Other, a)
			}
		}
		if r.Type == "" {
			return fmt.Errorf("%s has no Type", path)
		}
		t.Resources = append(t.Resources, r)
	}
	return nil
}

func (t *Template) buildOutputs(n Node) error {
	entries, err := section("Outputs", n)
	if err != nil {
		return err
	}
	for _, f := range entries {
		path := "Outputs." + f.Key
		if isLoop(f.Key) {
			t.Loops = append(t.Loops, Field{Key: path, Value: f.Value})
			continue
		}
		m, ok := f.Value.(Map)
		if !ok {
			return fmt.Errorf("%s must be a mapping", path)
		}
		o := &Output{Name: f.Key}
		for _, a := range m {
			switch a.Key {
			case "Description":
				o.Description = text(a.Value)
			case "Value":
				o.Value = a.Value
			case "Condition":
				o.Condition = text(a.Value)
			case "Export":
				export, ok := a.Value.(Map)
				if !ok {
					return fmt.Errorf("%s.Export must be a mapping with a Name", path)
				}
				if name, ok := export.Get("Name"); ok {
					o.Export = name
				}
			}
		}
		t.Outputs = append(t.Outputs, o)
	}
	return nil
}

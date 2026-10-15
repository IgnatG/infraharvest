// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Template builds an import ID from text and fields in braces:
//
//	{id}              the ID the lister recorded
//	{subnet_id}       an attribute the lister recorded
//	{id|after_last:/} a field through filters, applied in order
//
// The filters are after_last:<s> and before_last:<s>, which keep what
// follows or precedes the last s (the whole value if it has no s), and
// trim_prefix:<s> and trim_suffix:<s>. A template renders only if every
// field has a value once filtered; braces can't be escaped, since no
// import ID has them.
type Template struct {
	source string
	parts  []templatePart
}

type templatePart struct {
	text    string // literal text, if field is ""
	field   string
	filters []filter
}

type filter struct {
	name, arg string
}

var filters = map[string]func(value, arg string) string{
	"after_last": func(v, arg string) string {
		if _, after, found := strings.CutLast(v, arg); found {
			return after
		}
		return v
	},
	"before_last": func(v, arg string) string {
		before, _, _ := strings.CutLast(v, arg) // all of v if arg is not in it
		return before
	},
	"trim_prefix": strings.TrimPrefix,
	"trim_suffix": strings.TrimSuffix,
}

// ParseTemplate parses an import ID template.
func ParseTemplate(source string) (Template, error) {
	t := Template{source: source}
	rest := source
	for rest != "" {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			if strings.IndexByte(rest, '}') >= 0 {
				return Template{}, fmt.Errorf("template %q: } without {", source)
			}
			t.parts = append(t.parts, templatePart{text: rest})
			break
		}
		if strings.IndexByte(rest[:open], '}') >= 0 {
			return Template{}, fmt.Errorf("template %q: } without {", source)
		}
		if open > 0 {
			t.parts = append(t.parts, templatePart{text: rest[:open]})
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return Template{}, fmt.Errorf("template %q: { without }", source)
		}
		part, err := parseField(rest[open+1 : open+end])
		if err != nil {
			return Template{}, fmt.Errorf("template %q: %w", source, err)
		}
		t.parts = append(t.parts, part)
		rest = rest[open+end+1:]
	}
	if len(t.parts) == 0 {
		return Template{}, errors.New("empty template")
	}
	return t, nil
}

func parseField(spec string) (templatePart, error) {
	pieces := strings.Split(spec, "|")
	part := templatePart{field: strings.TrimSpace(pieces[0])}
	if part.field == "" || strings.ContainsAny(part.field, " {:") {
		return templatePart{}, fmt.Errorf("field %q: want {id} or {<attribute>}", spec)
	}
	for _, f := range pieces[1:] {
		name, arg, ok := strings.Cut(f, ":")
		if _, known := filters[name]; !known {
			return templatePart{}, fmt.Errorf("filter %q: want after_last, before_last, trim_prefix or trim_suffix", f)
		}
		if !ok || arg == "" {
			return templatePart{}, fmt.Errorf("filter %q: want %s:<text>", f, name)
		}
		part.filters = append(part.filters, filter{name, arg})
	}
	return part, nil
}

// Render fills the template in from a resource's lister ID and attributes,
// and reports whether every field had a value.
func (t Template) Render(id string, attributes map[string]string) (string, bool) {
	var b strings.Builder
	for _, p := range t.parts {
		if p.field == "" {
			b.WriteString(p.text)
			continue
		}
		value := attributes[p.field]
		if p.field == "id" {
			value = id
		}
		for _, f := range p.filters {
			value = filters[f.name](value, f.arg)
		}
		if value == "" {
			return "", false
		}
		b.WriteString(value)
	}
	return b.String(), true
}

// Fields names the template's fields.
func (t Template) Fields() []string {
	var fields []string
	for _, p := range t.parts {
		if p.field != "" {
			fields = append(fields, p.field)
		}
	}
	return fields
}

func (t Template) String() string {
	return t.source
}

// UnmarshalYAML parses a template from a YAML string.
func (t *Template) UnmarshalYAML(node *yaml.Node) error {
	var source string
	if err := node.Decode(&source); err != nil {
		return err
	}
	parsed, err := ParseTemplate(source)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*t = parsed
	return nil
}

// MarshalYAML writes the template as its source.
func (t Template) MarshalYAML() (any, error) {
	return t.source, nil
}

func templateList(templates Templates) string {
	sources := make([]string, len(templates))
	for i, t := range templates {
		sources[i] = t.source
	}
	return strings.Join(sources, ", ")
}

// Templates are import ID templates in order of preference. In YAML they
// are a list, or one template as a string.
type Templates []Template

// UnmarshalYAML reads a template or a list of them.
func (ts *Templates) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var t Template
		if err := node.Decode(&t); err != nil {
			return err
		}
		*ts = Templates{t}
		return nil
	}
	var list []Template
	if err := node.Decode(&list); err != nil {
		return err
	}
	*ts = list
	return nil
}

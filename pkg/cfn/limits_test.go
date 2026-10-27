// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"encoding/json"
	"strings"
	"testing"
)

// A template nested maxDepth deep parses and converts, in JSON and in
// YAML, where a tagged mapping that is a function makes two levels of
// values of one; a level deeper fails to parse.
func TestParseDepthLimit(t *testing.T) {
	// Other is one level down, so maxDepth-1 more reach maxDepth.
	jsonTemplate := func(levels int) string {
		return `{"Resources": {}, "Other": ` + nested(levels, `[%s]`, `0`) + `}`
	}
	yamlTemplate := func(levels int) string {
		return "Resources: {}\nOther: " + nested(levels, `!Base64 {Fn::Base64: %s}`, `x`) + "\n"
	}
	for name, template := range map[string]func(int) string{"JSON": jsonTemplate, "YAML": yamlTemplate} {
		t.Run(name, func(t *testing.T) {
			res := mustConvert(t, template(maxDepth-1), Options{})
			if !hasDiagnostic(res, Warning, "Other", "isn't converted") {
				t.Errorf("diagnostics: %v", res.Diagnostics)
			}
			if _, err := Parse(strings.NewReader(template(maxDepth))); err == nil || !strings.Contains(err.Error(), "too deeply") {
				t.Errorf("err = %v, want the depth limit's", err)
			}
		})
	}
}

// A Template built by hand, rather than by Parse, can hold a value that
// contains itself, or one nested or repeated past what the converter
// follows; Convert and ConvertValue fail on it rather than overflow the
// stack or loop forever. So does a nil entry, which Convert would
// dereference.
func TestHandBuiltValuesAreBounded(t *testing.T) {
	selfList := List{String("a"), nil}
	selfList[1] = selfList
	selfMap := Map{{Key: "Name", Value: nil}}
	selfMap[0].Value = selfMap
	// Fn::If whose false branch is the Fn::If itself.
	selfIf := make(List, 3)
	selfIf[0], selfIf[1] = String("C"), String("a")
	selfIf[2] = Func{Name: "Fn::If", Arg: selfIf}
	chain := func(levels int) Node {
		var n Node = String("x")
		for i := 0; i < levels; i++ {
			n = Func{Name: "Fn::Base64", Arg: n}
		}
		return n
	}
	// Each level holds the one below twice: 2^40 values, 40 deep.
	var wide Node = String("x")
	for i := 0; i < 40; i++ {
		wide = List{wide, wide}
	}

	topic := func(name Node) *Template {
		return &Template{Resources: []*Resource{{LogicalID: "T", Type: "AWS::SNS::Topic", Properties: Map{{Key: "TopicName", Value: name}}}}}
	}
	tests := []struct {
		name  string
		value Node
		want  string
	}{
		{"list that contains itself", selfList, "contains itself"},
		{"mapping that contains itself", selfMap, "contains itself"},
		{"function that contains itself", Func{Name: "Fn::If", Arg: selfIf}, "contains itself"},
		{"too deep", chain(maxValueDepth + 1), "more than 1026 deep"},
		{"too many values", wide, "more than 16777216 values"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Convert(topic(tt.value), Options{}); err == nil || !strings.Contains(err.Error(), "Resources.T.Properties") || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Convert: err = %v, want one about Resources.T.Properties containing %q", err, tt.want)
			}
			if _, err := ConvertValue(topic(nil), tt.value, "Outputs.X.Value", Options{}); err == nil || !strings.Contains(err.Error(), "Outputs.X.Value") {
				t.Errorf("ConvertValue: err = %v, want one about Outputs.X.Value", err)
			}
		})
	}

	// As deep as the converter follows converts: TopicName is a level below
	// Properties.
	if _, err := Convert(topic(chain(maxValueDepth-1)), Options{}); err != nil {
		t.Errorf("Convert: %v", err)
	}

	nilEntry := topic(String("x"))
	nilEntry.Outputs = []*Output{{Name: "A", Value: String("a")}, nil}
	for _, convert := range []func() error{
		func() error { _, err := Convert(nilEntry, Options{}); return err },
		func() error { _, err := ConvertValue(nilEntry, String("x"), "", Options{}); return err },
	} {
		if err := convert(); err == nil || !strings.Contains(err.Error(), "Outputs[1] is nil") {
			t.Errorf("err = %v, want the nil entry's", err)
		}
	}
}

// MarshalJSON of a Map or Func that contains itself fails rather than
// recurse until the stack overflows, and of a sound one still writes JSON.
func TestMarshalJSONIsBounded(t *testing.T) {
	selfMap := Map{{Key: "A", Value: nil}}
	selfMap[0].Value = List{selfMap}
	selfFunc := List{nil}
	selfFunc[0] = Func{Name: "Fn::Base64", Arg: selfFunc}
	for name, v := range map[string]any{"map": selfMap, "function": selfFunc[0], "list": selfFunc} {
		if _, err := json.Marshal(v); err == nil {
			t.Errorf("%s: json.Marshal succeeded, want an error", name)
		}
	}
	got, err := json.Marshal(Map{{Key: "a", Value: List{Func{Name: "Ref", Arg: String("X")}, Number("0x1F"), Null{}, Bool(true), nil}}})
	if want := `{"a":[{"Ref":"X"},"0x1F",null,true,null]}`; err != nil || string(got) != want {
		t.Errorf("json.Marshal = %s, %v; want %s", got, err, want)
	}
}

// The lookups skip a nil entry rather than dereference it.
func TestLookupsSkipNilEntries(t *testing.T) {
	tmpl := &Template{Parameters: []*Parameter{nil, {Name: "P"}}, Resources: []*Resource{nil, {LogicalID: "R"}}}
	if p := tmpl.Parameter("P"); p == nil || p.Name != "P" {
		t.Errorf("Parameter(P) = %v", p)
	}
	if r := tmpl.Resource("R"); r == nil || r.LogicalID != "R" {
		t.Errorf("Resource(R) = %v", r)
	}
}

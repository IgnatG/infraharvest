// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func mustParse(t *testing.T, src string) *Template {
	t.Helper()
	tmpl, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return tmpl
}

// mustNode parses a YAML or JSON value on its own.
func mustNode(t *testing.T, src string) Node {
	t.Helper()
	n, err := parseDocument([]byte(src))
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}

// Each YAML short-form tag parses to the same node as its long form.
func TestShortForms(t *testing.T) {
	tests := []struct {
		short, long string
	}{
		{`!Ref Bucket`, `{"Ref": "Bucket"}`},
		{`!Sub "${AWS::Region}-x"`, `{"Fn::Sub": "${AWS::Region}-x"}`},
		{`!Sub ["${A}", {A: b}]`, `{"Fn::Sub": ["${A}", {"A": "b"}]}`},
		{`!GetAtt Bucket.Arn`, `{"Fn::GetAtt": ["Bucket", "Arn"]}`},
		{`!GetAtt Db.Endpoint.Address`, `{"Fn::GetAtt": ["Db", "Endpoint.Address"]}`},
		{`!GetAtt [Bucket, Arn]`, `{"Fn::GetAtt": ["Bucket", "Arn"]}`},
		{`!If [IsProd, a, !Ref AWS::NoValue]`, `{"Fn::If": ["IsProd", "a", {"Ref": "AWS::NoValue"}]}`},
		{`!Join [",", [a, b]]`, `{"Fn::Join": [",", ["a", "b"]]}`},
		{`!Select [0, !GetAZs ""]`, `{"Fn::Select": [0, {"Fn::GetAZs": ""}]}`},
		{`!Split [",", "a,b"]`, `{"Fn::Split": [",", "a,b"]}`},
		{`!FindInMap [M, k1, k2]`, `{"Fn::FindInMap": ["M", "k1", "k2"]}`},
		{`!Base64 text`, `{"Fn::Base64": "text"}`},
		{`!Cidr ["10.0.0.0/16", 4, 8]`, `{"Fn::Cidr": ["10.0.0.0/16", 4, 8]}`},
		{`!ImportValue Shared`, `{"Fn::ImportValue": "Shared"}`},
		{`!Equals [a, b]`, `{"Fn::Equals": ["a", "b"]}`},
		{`!And [!Condition A, !Condition B]`, `{"Fn::And": [{"Condition": "A"}, {"Condition": "B"}]}`},
		{`!Or [!Condition A, !Condition B]`, `{"Fn::Or": [{"Condition": "A"}, {"Condition": "B"}]}`},
		{`!Not [!Condition A]`, `{"Fn::Not": [{"Condition": "A"}]}`},
		{`!Transform {Name: AWS::Include, Parameters: {Location: s3://b/k}}`, `{"Fn::Transform": {"Name": "AWS::Include", "Parameters": {"Location": "s3://b/k"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.short, func(t *testing.T) {
			short := mustNode(t, tt.short)
			long := mustNode(t, tt.long)
			// The JSON form of {"Condition": X} is a Map; the tag is a Func.
			// Both convert alike; compare them as the converter reads them.
			if !reflect.DeepEqual(normalizeConditions(short), normalizeConditions(long)) {
				t.Errorf("got %s, want %s", compactJSON(short), compactJSON(long))
			}
		})
	}
}

// normalizeConditions turns each {Condition: X} Map into the Func the
// !Condition tag parses to.
func normalizeConditions(n Node) Node {
	switch v := n.(type) {
	case List:
		out := make(List, len(v))
		for i, e := range v {
			out[i] = normalizeConditions(e)
		}
		return out
	case Map:
		if len(v) == 1 && v[0].Key == "Condition" {
			return Func{Name: "Condition", Arg: normalizeConditions(v[0].Value)}
		}
		out := make(Map, len(v))
		for i, f := range v {
			out[i] = Field{Key: f.Key, Value: normalizeConditions(f.Value)}
		}
		return out
	case Func:
		return Func{Name: v.Name, Arg: normalizeConditions(v.Arg)}
	}
	return n
}

func TestScalars(t *testing.T) {
	tests := []struct {
		src  string
		want Node
	}{
		{`"10"`, String("10")},
		{`10`, Number("10")},
		{`1.5`, Number("1.5")},
		{`true`, Bool(true)},
		{`~`, Null{}},
		{`2010-09-09`, String("2010-09-09")},
		{`!!str 10`, String("10")},
		{`[1, "a", null, false]`, List{Number("1"), String("a"), Null{}, Bool(false)}},
	}
	for _, tt := range tests {
		if got := mustNode(t, tt.src); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %#v, want %#v", tt.src, got, tt.want)
		}
	}
}

// A JSON object keeps its keys' order, and an IAM policy's Condition
// stays a mapping.
func TestJSONOrderAndCondition(t *testing.T) {
	n := mustNode(t, `{"b": 1, "a": {"Condition": {"StringEquals": {"k": "v"}}}, "c": [{"Ref": "X"}]}`)
	m, ok := n.(Map)
	if !ok {
		t.Fatalf("got %T, want Map", n)
	}
	if got := strings.Join(m.Keys(), ","); got != "b,a,c" {
		t.Errorf("keys = %s, want b,a,c", got)
	}
	a, _ := m.Get("a")
	if _, ok := a.(Map); !ok {
		t.Errorf("a is %T, want Map", a)
	}
	c, _ := m.Get("c")
	if want := (List{Func{Name: "Ref", Arg: String("X")}}); !reflect.DeepEqual(c, want) {
		t.Errorf("c = %#v, want %#v", c, want)
	}
}

func TestYAMLAliasesAndMerge(t *testing.T) {
	n := mustNode(t, `
base: &base {a: 1, b: 2}
derived:
  <<: *base
  b: 3
copy: *base
`)
	m := n.(Map)
	derived, _ := m.Get("derived")
	want := Map{{Key: "b", Value: Number("3")}, {Key: "a", Value: Number("1")}}
	if !reflect.DeepEqual(derived, want) {
		t.Errorf("derived = %s, want %s", compactJSON(derived), compactJSON(want))
	}
	cp, _ := m.Get("copy")
	if got := compactJSON(cp); got != `{"a":1,"b":2}` {
		t.Errorf("copy = %s", got)
	}
}

func TestParseTemplate(t *testing.T) {
	tmpl := mustParse(t, `
AWSTemplateFormatVersion: 2010-09-09
Description: A test.
Transform: AWS::LanguageExtensions
Parameters:
  Env:
    Type: String
    Default: dev
    AllowedValues: [dev, prod]
    NoEcho: "true"
    MinLength: 2
Conditions:
  IsProd: !Equals [!Ref Env, prod]
Mappings:
  M:
    k: {v: 1}
Resources:
  Bucket:
    Type: AWS::S3::Bucket
    Condition: IsProd
    DependsOn: Other
    DeletionPolicy: Retain
    UpdatePolicy: {x: 1}
    Properties:
      BucketName: b
  Other:
    Type: AWS::SNS::Topic
  Fn::ForEach::Topics:
    - T
    - [a, b]
    - Topic${T}: {Type: AWS::SNS::Topic}
Outputs:
  Name:
    Description: The name.
    Value: !Ref Bucket
    Condition: IsProd
    Export:
      Name: !Sub "${AWS::StackName}-Name"
Hooks:
  H: {}
`)
	if tmpl.FormatVersion != "2010-09-09" || tmpl.Description != "A test." {
		t.Errorf("FormatVersion %q, Description %q", tmpl.FormatVersion, tmpl.Description)
	}
	if !reflect.DeepEqual(tmpl.Transform, []string{"AWS::LanguageExtensions"}) {
		t.Errorf("Transform = %v", tmpl.Transform)
	}
	p := tmpl.Parameter("Env")
	if p == nil || p.Type != "String" || !p.NoEcho || p.MinLength != "2" || len(p.AllowedValues) != 2 || p.Default != String("dev") {
		t.Errorf("Env = %+v", p)
	}
	r := tmpl.Resource("Bucket")
	if r == nil || r.Condition != "IsProd" || !reflect.DeepEqual(r.DependsOn, []string{"Other"}) || r.DeletionPolicy != String("Retain") {
		t.Fatalf("Bucket = %+v", r)
	}
	if len(r.Other) != 1 || r.Other[0].Key != "UpdatePolicy" {
		t.Errorf("Bucket.Other = %v", r.Other)
	}
	if len(tmpl.Resources) != 2 || len(tmpl.Loops) != 1 || tmpl.Loops[0].Key != "Resources.Fn::ForEach::Topics" {
		t.Errorf("Resources %d, Loops %v", len(tmpl.Resources), tmpl.Loops)
	}
	o := tmpl.Outputs[0]
	if o.Condition != "IsProd" || o.Description != "The name." || o.Export == nil {
		t.Errorf("Output = %+v", o)
	}
	if len(tmpl.Other) != 1 || tmpl.Other[0].Key != "Hooks" {
		t.Errorf("Other = %v", tmpl.Other)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"empty", "  \n", "empty"},
		{"not a mapping", "[1, 2]", "mapping of sections"},
		{"no resources", "Parameters: {}", "no Resources"},
		{"no type", "Resources:\n  A:\n    Properties: {}\n", "has no Type"},
		{"resource not a mapping", "Resources:\n  A: 1\n", "must be a mapping"},
		{"bad JSON", `{"Resources": {`, "JSON"},
		{"trailing JSON", `{"Resources": {}} {}`, "more after"},
		{"bad DependsOn", "Resources:\n  A:\n    Type: X\n    DependsOn: {a: 1}\n", "DependsOn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.src))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// Aliases that expand to more values than the budget fail rather than
// exhaust memory.
func TestAliasBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i < 9; i++ {
		prev := i - 1
		b.WriteString("a" + string(rune('0'+i)) + ": &a" + string(rune('0'+i)) + " [")
		for j := 0; j < 10; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("*a" + string(rune('0'+prev)))
		}
		b.WriteString("]\n")
	}
	b.WriteString("Resources: {}\n")
	if _, err := Parse(strings.NewReader(b.String())); err == nil || !strings.Contains(err.Error(), "too many values") {
		t.Errorf("err = %v, want too many values", err)
	}
}

// The template's keys and strings reach a diagnostic's text, or a parse
// error's, with their control characters escaped: printing either can't
// send a terminal an escape sequence the template carries.
func TestDiagnosticsEscapeControlCharacters(t *testing.T) {
	tmpl := mustParse(t, `{"Resources": {"Bad\u001b[2J": {"Type": "AWS::S3::Bucket", "DependsOn": "Gone\u001b]0;x\u0007"}}}`)
	res, err := Convert(tmpl, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range res.Diagnostics {
		text := d.String()
		if strings.ContainsFunc(text, unicode.IsControl) {
			t.Errorf("diagnostic %q holds control characters", text)
		}
		if strings.Contains(text, `Bad\x1b[2J`) && strings.Contains(text, `Gone\x1b]0;x\a`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no diagnostic names the escaped logical IDs: %v", res.Diagnostics)
	}

	_, err = Parse(strings.NewReader("{\"Resources\": {\"Bad\\u001b[2J\": {}}}"))
	if err == nil || strings.ContainsFunc(err.Error(), unicode.IsControl) || !strings.Contains(err.Error(), `Bad\x1b[2J`) {
		t.Errorf("Parse: %v, want the key escaped", err)
	}
	if got := printable("ok\xffé\u009b"); got != `ok`+"�"+`é\u009b` {
		t.Errorf("printable: %q", got)
	}
}

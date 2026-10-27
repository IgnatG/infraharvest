// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parsing any template doesn't panic or hang, nor does converting what it
// parses, as a whole or one output at a time; and the converter never
// writes HCL that doesn't parse, which it reports as a bug.
func FuzzParseConvert(f *testing.F) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		f.Fatal(err)
	}
	for _, input := range inputs {
		if ext := filepath.Ext(input); ext != ".yaml" && ext != ".json" {
			continue
		}
		src, err := os.ReadFile(input)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(src)
	}
	f.Add([]byte(`{"Resources": {"T": {"Type": "AWS::SNS::Topic", "Properties": {"TopicName": {"Fn::Sub": ["${A}-${AWS::Region}", {"A": {"Fn::If": ["C", "a", {"Ref": "AWS::NoValue"}]}}]}}}}, "Conditions": {"C": {"Fn::Not": [{"Fn::Equals": [{"Ref": "AWS::Region"}, "us-east-1"]}]}}}`))
	f.Add([]byte("Base: &base {Type: AWS::S3::Bucket, Properties: {Tags: [{Key: a, Value: !GetAtt B.Arn}]}}\nResources:\n  A: *base\n  B:\n    <<: *base\n    Condition: C\nOutputs:\n  O: {Value: !Join [\",\", [!Ref A, !Select [0, !GetAZs \"\"]]], Export: {Name: !Sub \"${AWS::StackName}-o\"}}\n"))
	f.Add([]byte("Parameters:\n  P: {Type: Number, AllowedValues: [1, 2], MinValue: 0, NoEcho: true}\nResources:\n  N:\n    Type: AWS::EC2::Subnet\n    Properties:\n      CidrBlock: !Select [0, !Cidr [!Ref P, 4, 8]]\n      VpcId: '{{resolve:ssm:/vpc/id}}'\n"))
	f.Add([]byte(`{"Transform": "AWS::LanguageExtensions", "Resources": {"Fn::ForEach::X": ["T", ["a"], {}]}, "Other": 1}`))
	f.Fuzz(func(t *testing.T, content []byte) {
		tmpl, err := Parse(strings.NewReader(string(content)))
		if err != nil {
			return
		}
		res, err := Convert(tmpl, Options{})
		if err != nil {
			if strings.Contains(err.Error(), "bug") {
				t.Fatalf("Convert: %v", err)
			}
			return
		}
		for _, d := range res.Diagnostics {
			if strings.Contains(d.Message, "which is a bug") {
				t.Errorf("Convert: %s", d)
			}
		}
		for _, o := range tmpl.Outputs {
			v, err := ConvertValue(tmpl, o.Value, "Outputs."+o.Name+".Value", Options{})
			if err != nil {
				continue
			}
			if !exprParses(v.Expr) {
				t.Errorf("ConvertValue of %s wrote an expression that doesn't parse: %s", o.Name, v.Expr)
			}
		}
	})
}

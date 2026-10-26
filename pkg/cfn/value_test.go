// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"reflect"
	"testing"
)

// fixedResolver resolves from maps.
type fixedResolver struct {
	resources, parameters map[string]string
}

func (r fixedResolver) Resource(id string) (string, bool) {
	a, ok := r.resources[id]
	return a, ok
}

func (r fixedResolver) Parameter(name string) (string, bool) {
	e, ok := r.parameters[name]
	return e, ok
}

func TestConvertValue(t *testing.T) {
	tmpl := mustParse(t, exprTemplate)
	resolver := fixedResolver{
		resources:  map[string]string{"Vpc": "aws_vpc.main", "Bucket": "aws_s3_bucket.logs", "Thing": "aws_foo_bar.x"},
		parameters: map[string]string{"Env": "var.environment", "AWS::StackName": `"app"`},
	}
	tests := []struct {
		name, src, want string
		data            []string
		errors          bool
		worst           Severity
	}{
		{name: "resource", src: `!GetAtt Bucket.Arn`, want: `aws_s3_bucket.logs.arn`, worst: -1},
		{name: "conditional resource keeps the resolver's address", src: `!Ref Bucket`, want: `aws_s3_bucket.logs.id`, worst: -1},
		{name: "parameter and pseudo parameters", src: `!Sub "${AWS::StackName}-${Env}-${AWS::Region}"`, want: `"app-${var.environment}-${data.aws_region.current.region}"`, data: []string{"data \"aws_region\" \"current\" {}\n"}, worst: -1},
		{name: "unresolved parameter", src: `!Ref Count`, want: `null`, errors: true, worst: Error},
		{name: "unresolved resource", src: `!GetAtt Db.Endpoint.Address`, want: `null`, errors: true, worst: Error},
		{name: "guessed attribute", src: `!GetAtt Thing.SomeAttr`, want: `aws_foo_bar.x.some_attr`, worst: Warning},
		{name: "condition", src: `!If [IsProd, a, b]`, want: `false ? "a" : "b"`, errors: true, worst: Error},
		{name: "mapping", src: `!FindInMap [RegionMap, a, b]`, want: `null`, errors: true, worst: Error},
		{name: "stand-in", src: `!Ref AWS::StackId`, want: `null`, errors: true, worst: Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ConvertValue(tmpl, mustNode(t, tt.src), "Outputs.X.Value", Options{Resolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			if v.Expr != tt.want {
				t.Errorf("Expr = %s, want %s", v.Expr, tt.want)
			}
			if !reflect.DeepEqual(v.Data, tt.data) {
				t.Errorf("Data = %q, want %q", v.Data, tt.data)
			}
			if v.HasErrors() != tt.errors || worst(v.Diagnostics) != tt.worst {
				t.Errorf("diagnostics: %v", v.Diagnostics)
			}
		})
	}
	if _, err := ConvertValue(nil, String("x"), "", Options{}); err == nil {
		t.Error("ConvertValue(nil) succeeded")
	}
}

func TestName(t *testing.T) {
	if got := Name("PublicSubnetA"); got != "public_subnet_a" {
		t.Errorf("Name = %q", got)
	}
}

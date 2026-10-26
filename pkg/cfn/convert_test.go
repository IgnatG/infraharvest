// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"bytes"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// update rewrites the golden files from what the converter does now:
//
//	go test ./pkg/cfn -run TestGolden -update
//
// or, without a local toolchain, the remote workflow's golden task.
var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func mustConvert(t *testing.T, src string, opts Options) *Result {
	t.Helper()
	res, err := Convert(mustParse(t, src), opts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for name, b := range res.Files {
		if _, diags := hclsyntax.ParseConfig(b, name, hcl.InitialPos); diags.HasErrors() {
			t.Errorf("%s doesn't parse: %v\n%s", name, diags, b)
		}
	}
	return res
}

var spaces = regexp.MustCompile(`[ \t]+`)

// squash collapses the alignment terraform fmt adds, so that a test can
// look for "type = string" whatever the attributes around it.
func squash(b []byte) string {
	return spaces.ReplaceAllString(string(b), " ")
}

func hasDiagnostic(res *Result, sev Severity, path, text string) bool {
	for _, d := range res.Diagnostics {
		if d.Severity == sev && d.Path == path && strings.Contains(d.Message, text) {
			return true
		}
	}
	return false
}

func TestParameters(t *testing.T) {
	tests := []struct {
		name, param string
		want        []string // lines variables.tf has, squashed
		data        string   // a line data.tf has
		diag        string   // a diagnostic's message, at Parameters.P or below
	}{
		{
			name:  "string with allowed values",
			param: "Type: String\nDefault: dev\nAllowedValues: [dev, prod]\nDescription: The environment.",
			want: []string{`variable "p" {`, `type = string`, `description = "The environment."`, `default = "dev"`,
				`condition = contains(["dev", "prod"], var.p)`, `error_message = "The value of P must be one of dev, prod."`},
		},
		{
			name:  "number with bounds",
			param: "Type: Number\nDefault: \"3\"\nMinValue: 1\nMaxValue: 10",
			want:  []string{`type = number`, `default = 3`, `condition = var.p >= 1`, `condition = var.p <= 10`, `error_message = "The value of P must be at most 10."`},
		},
		{
			name:  "list of numbers",
			param: "Type: List<Number>\nDefault: \"1, 2\"\nAllowedValues: [1, 2, 3]",
			want:  []string{`type = list(number)`, `default = [1, 2]`, `condition = alltrue([for v in var.p : contains([1, 2, 3], v)])`},
		},
		{
			name:  "comma-delimited list",
			param: "Type: CommaDelimitedList\nDefault: a,b",
			want:  []string{`type = list(string)`, `default = ["a", "b"]`},
		},
		{
			name:  "empty list default",
			param: "Type: CommaDelimitedList\nDefault: \"\"",
			want:  []string{`default = []`},
		},
		{
			name:  "AWS-specific type",
			param: "Type: AWS::EC2::VPC::Id",
			want:  []string{`type = string`, `description = "CloudFormation type AWS::EC2::VPC::Id."`},
		},
		{
			name:  "list of an AWS-specific type",
			param: "Type: List<AWS::EC2::Subnet::Id>\nDescription: Subnets.",
			want:  []string{`type = list(string)`, `description = "Subnets. (CloudFormation type List<AWS::EC2::Subnet::Id>)"`},
		},
		{
			name:  "SSM parameter",
			param: "Type: AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>\nDefault: /aws/service/ami",
			want:  []string{`type = string`, `default = "/aws/service/ami"`},
			data:  `name = var.p`,
		},
		{
			name:  "NoEcho",
			param: "Type: String\nNoEcho: true\nDefault: hunter2",
			want:  []string{`sensitive = true`},
			diag:  "NoEcho",
		},
		{
			name:  "pattern and lengths",
			param: "Type: String\nAllowedPattern: \"[a-z]+\"\nMinLength: 3\nMaxLength: 8\nConstraintDescription: lower-case letters",
			want: []string{`condition = can(regex("^(?:[a-z]+)$", var.p))`, `condition = length(var.p) >= 3`,
				`condition = length(var.p) <= 8`, `error_message = "Lower-case letters."`},
		},
		{
			name:  "pattern Terraform can't read",
			param: "Type: String\nAllowedPattern: \"(?=a)a\"",
			want:  []string{`type = string`},
			diag:  "RE2",
		},
		{
			name:  "unknown type",
			param: "Type: Strin",
			want:  []string{`type = string`},
			diag:  "isn't a parameter type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "Parameters:\n  P:\n    " + strings.ReplaceAll(tt.param, "\n", "\n    ") + "\nResources:\n  T:\n    Type: AWS::SNS::Topic\n    Properties:\n      TopicName: !Ref P\n"
			res := mustConvert(t, src, Options{})
			vars := squash(res.Files["variables.tf"])
			for _, line := range tt.want {
				if !strings.Contains(vars, line) {
					t.Errorf("variables.tf lacks %s:\n%s", line, vars)
				}
			}
			if strings.Contains(vars, "hunter2") {
				t.Errorf("variables.tf holds a NoEcho default:\n%s", vars)
			}
			if tt.data != "" && !strings.Contains(squash(res.Files["data.tf"]), tt.data) {
				t.Errorf("data.tf lacks %s:\n%s", tt.data, res.Files["data.tf"])
			}
			if tt.diag != "" {
				found := false
				for _, d := range res.Diagnostics {
					found = found || strings.HasPrefix(d.Path, "Parameters.P") && strings.Contains(d.Message, tt.diag)
				}
				if !found {
					t.Errorf("no diagnostic about %q: %v", tt.diag, res.Diagnostics)
				}
			}
		})
	}
}

// A parameter named like a Terraform meta-argument, or like a stand-in
// variable, gets a name of its own.
func TestVariableNames(t *testing.T) {
	res := mustConvert(t, `
Parameters:
  Version: {Type: String}
  StackName: {Type: String}
Resources:
  T:
    Type: AWS::SNS::Topic
    Properties:
      TopicName: !Sub "${Version}-${StackName}-${AWS::StackName}"
`, Options{StackName: "prod"})
	vars := squash(res.Files["variables.tf"])
	for _, want := range []string{`variable "version_value"`, `variable "stack_name"`, `variable "stack_name_2"`, `default = "prod"`} {
		if !strings.Contains(vars, want) {
			t.Errorf("variables.tf lacks %s:\n%s", want, vars)
		}
	}
	if main := squash(res.Files["main.tf"]); !strings.Contains(main, `name = "${var.version_value}-${var.stack_name}-${var.stack_name_2}"`) {
		t.Errorf("main.tf:\n%s", main)
	}
}

func TestResources(t *testing.T) {
	res := mustConvert(t, `
Conditions:
  HasLogs: !Equals [a, a]
Resources:
  Logs:
    Type: AWS::S3::Bucket
    Condition: HasLogs
    DeletionPolicy: Retain
    Properties:
      BucketName: logs
      VersioningConfiguration: {Status: Enabled}
      Tags:
        - {Key: team, Value: web}
        - {Key: !Ref AWS::Region, Value: x}
  Topic:
    Type: AWS::SNS::Topic
    DependsOn: [Logs, Hook]
    Properties:
      TopicName: !Ref Logs
  Hook:
    Type: Custom::Hook
    Properties:
      ServiceToken: arn:aws:lambda:x
  Group:
    Type: AWS::EC2::SecurityGroup
    Properties:
      GroupDescription: Web.
      SecurityGroupIngress:
        - {IpProtocol: tcp, FromPort: 443, ToPort: 443, CidrIp: 0.0.0.0/0}
        - !If [HasLogs, {IpProtocol: tcp, FromPort: 80, ToPort: 80, CidrIp: 0.0.0.0/0}, !Ref AWS::NoValue]
`, Options{PreventDestroy: true})
	main := squash(res.Files["main.tf"])
	for _, want := range []string{
		`resource "aws_s3_bucket" "logs" {`,
		`count = local.has_logs ? 1 : 0`,
		`bucket = "logs"`,
		`team = "web"`,
		`(data.aws_region.current.region) = "x"`,
		`prevent_destroy = true`,
		`# warning: VersioningConfiguration is left out: the AWS provider manages it with a separate aws_s3_bucket_versioning resource`,
		`# It was: {"Status":"Enabled"}`,
		`# DeletionPolicy Retain:`,
		`name = aws_s3_bucket.logs[0].id`,
		`depends_on = [aws_s3_bucket.logs]`,
		`# Hook (Custom::Hook)`,
		`# It was: {"Type":"Custom::Hook","Properties":{"ServiceToken":"arn:aws:lambda:x"}}`,
		"ingress {\n protocol = \"tcp\"",
		`cidr_blocks = ["0.0.0.0/0"]`,
		`dynamic "ingress" {`,
		`for_each = local.has_logs ? [1] : []`,
		`content {`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf lacks %s:\n%s", want, main)
		}
	}
	if !hasDiagnostic(res, Error, "Resources.Hook", "custom resource") {
		t.Errorf("no error for the custom resource: %v", res.Diagnostics)
	}
	if !hasDiagnostic(res, Warning, "Resources.Topic.DependsOn", "Hook") {
		t.Errorf("no warning for the dependency on the custom resource: %v", res.Diagnostics)
	}
	if !res.HasErrors() {
		t.Error("HasErrors is false")
	}
}

func TestOutputs(t *testing.T) {
	res := mustConvert(t, `
Parameters:
  Password: {Type: String, NoEcho: true}
Conditions:
  Enabled: !Equals [a, a]
Resources:
  Topic: {Type: AWS::SNS::Topic, Condition: Enabled}
Outputs:
  TopicArn:
    Description: The topic.
    Condition: Enabled
    Value: !Ref Topic
    Export: {Name: !Sub "${AWS::StackName}-Topic"}
  Secret:
    Value: !Ref Password
`, Options{})
	out := squash(res.Files["outputs.tf"])
	for _, want := range []string{
		`output "topic_arn" {`,
		`description = "The topic."`,
		`value = local.enabled ? aws_sns_topic.topic[0].arn : null`,
		`# CloudFormation exports this value as {"Fn::Sub":"${AWS::StackName}-Topic"}.`,
		`value = var.password`,
		`sensitive = true`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("outputs.tf lacks %s:\n%s", want, out)
		}
	}
}

// strictMapper knows one type and one property, and wraps nothing else, to
// show how a caller's mapper replaces the default one.
type strictMapper struct{}

func (strictMapper) ResourceType(t string) (Mapped, bool) {
	if t == "Acme::Widget::Thing" {
		return Mapped{Name: "acme_widget"}, true
	}
	return Mapped{}, false
}

func (strictMapper) Property(_ string, path []string) (Property, bool) {
	if len(path) == 1 && path[0] == "Size" {
		return Property{Name: "size"}, true
	}
	return Property{}, false
}

func (strictMapper) Ref(string) (Mapped, bool) { return Mapped{Name: "widget_id"}, true }

func (strictMapper) Attribute(_, attr string) (Mapped, bool) {
	return Mapped{Name: strings.ToLower(attr)}, true
}

func TestCustomMapper(t *testing.T) {
	res := mustConvert(t, `
Resources:
  W:
    Type: Acme::Widget::Thing
    Properties:
      Size: 3
      Colour: red
  V:
    Type: AWS::EC2::VPC
Outputs:
  Id: {Value: !Ref W}
  Arn: {Value: !GetAtt W.Arn}
`, Options{Mapper: strictMapper{}})
	main := squash(res.Files["main.tf"])
	for _, want := range []string{`resource "acme_widget" "w" {`, `size = 3`, `# error: the mapper has no Terraform argument for Colour; it is left out`, `# V (AWS::EC2::VPC)`} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf lacks %s:\n%s", want, main)
		}
	}
	out := squash(res.Files["outputs.tf"])
	for _, want := range []string{`value = acme_widget.w.widget_id`, `value = acme_widget.w.arn`} {
		if !strings.Contains(out, want) {
			t.Errorf("outputs.tf lacks %s:\n%s", want, out)
		}
	}
	if !hasDiagnostic(res, Error, "Resources.V", "no Terraform resource type") {
		t.Errorf("no error for the unmapped type: %v", res.Diagnostics)
	}
}

// nested returns a JSON value that wraps inner depth times in wrap, whose
// %s is where the next level goes.
func nested(depth int, wrap, inner string) string {
	v := inner
	for i := 0; i < depth; i++ {
		v = fmt.Sprintf(wrap, v)
	}
	return v
}

func topicTemplate(name string) string {
	return `{"Resources": {"T": {"Type": "AWS::SNS::Topic", "Properties": {"TopicName": ` + name + `}}}}`
}

// widgetMapper embeds BaseMapper and answers only for its own type, as
// BaseMapper's documentation says a caller's mapper should.
type widgetMapper struct{ BaseMapper }

func (m widgetMapper) ResourceType(t string) (Mapped, bool) {
	if t == "Acme::Widget::Thing" {
		return Mapped{Name: "acme_widget"}, true
	}
	return m.BaseMapper.ResourceType(t)
}

func TestEmbeddedBaseMapper(t *testing.T) {
	res := mustConvert(t, `
Resources:
  W:
    Type: Acme::Widget::Thing
  V:
    Type: AWS::EC2::VPC
    Properties:
      CidrBlock: 10.0.0.0/16
`, Options{Mapper: widgetMapper{}})
	main := squash(res.Files["main.tf"])
	for _, want := range []string{`resource "acme_widget" "w" {`, `resource "aws_vpc" "v" {`, `cidr_block = "10.0.0.0/16"`} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf lacks %s:\n%s", want, main)
		}
	}
}

// Fn::Sub variables that each use the one inside four times would write
// 4^depth copies of the innermost value; the conversion stops at its
// budget and fails instead.
func TestNestedSubIsBounded(t *testing.T) {
	src := topicTemplate(nested(40, `{"Fn::Sub": ["${A}${A}${A}${A}", {"A": %s}]}`, `"x"`))
	tmpl := mustParse(t, src)
	_, err := Convert(tmpl, Options{})
	if err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("err = %v, want the output budget's", err)
	}
	// A shallow nesting is fine, and each variable is converted once.
	res := mustConvert(t, topicTemplate(nested(3, `{"Fn::Sub": ["${A}-${A}", {"A": %s}]}`, `{"Ref": "AWS::Region"}`)), Options{})
	if got := squash(res.Files["main.tf"]); !strings.Contains(got, `name = "${"${"${data.aws_region.current.region}-${data.aws_region.current.region}"}-`) {
		t.Errorf("main.tf:\n%s", got)
	}
}

// A nested Fn::Cidr writes its block once per level, so the output grows
// with the depth, not exponentially.
func TestNestedCidrIsBounded(t *testing.T) {
	const depth = 60
	src := topicTemplate(nested(depth, `{"Fn::Select": [0, {"Fn::Cidr": [%s, 2, 1]}]}`, `{"Ref": "AWS::Region"}`))
	res := mustConvert(t, src, Options{})
	if n := len(res.Files["main.tf"]); n > depth*200 {
		t.Errorf("main.tf is %d bytes for %d levels", n, depth)
	}
}

func TestConvertNil(t *testing.T) {
	if _, err := Convert(nil, Options{}); err == nil {
		t.Error("Convert(nil) succeeded")
	}
}

// renderResult writes a result as one golden file: each file under a
// header, then the diagnostics as comments.
func renderResult(res *Result) []byte {
	var b bytes.Buffer
	for _, name := range res.FileNames() {
		fmt.Fprintf(&b, "# ---- %s ----\n\n", name)
		b.Write(res.Files[name])
		b.WriteString("\n")
	}
	b.WriteString("# ---- diagnostics ----\n")
	for _, d := range res.Diagnostics {
		fmt.Fprintf(&b, "# %s\n", d)
	}
	return b.Bytes()
}

// Each template in testdata converts to its golden file,
// testdata/<name>.golden.tf, and does so the same way each time.
func TestGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, input := range inputs {
		ext := filepath.Ext(input)
		if ext != ".yaml" && ext != ".json" {
			continue
		}
		ran++
		name := strings.TrimSuffix(filepath.Base(input), ext)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			res := mustConvert(t, string(src), Options{})
			got := renderResult(res)
			if again := renderResult(mustConvert(t, string(src), Options{})); !bytes.Equal(got, again) {
				t.Errorf("a second conversion differs:\n%s", again)
			}
			golden := filepath.Join("testdata", name+".golden.tf")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v: run go test ./pkg/cfn -run TestGolden -update", err)
			}
			if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
				t.Errorf("%s differs; got:\n%s", golden, got)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no templates in testdata")
	}
}

// The package imports nothing else of infraharvest's but internal/cfnhcl,
// which imports nothing of infraharvest's itself, so that the two can move
// to a module of their own.
func TestNoInternalImports(t *testing.T) {
	const shared = "github.com/IgnatG/infraharvest/internal/cfnhcl"
	for dir, allowed := range map[string][]string{
		".":                     {"github.com/IgnatG/infraharvest/pkg/cfn", shared},
		"../../internal/cfnhcl": {shared},
	} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files in %s", dir)
		}
		fset := token.NewFileSet()
		for _, file := range files {
			f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(path, "github.com/IgnatG/infraharvest") && !slices.Contains(allowed, path) {
					t.Errorf("%s imports %s", file, path)
				}
			}
		}
	}
}

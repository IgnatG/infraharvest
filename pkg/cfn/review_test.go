// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"strings"
	"testing"
)

// Dynamic references become reads of what they name, or null with an
// error; never the reference's text, which CloudFormation would replace.
func TestDynamicReferences(t *testing.T) {
	tests := []struct {
		name, src, want string
		data            string // in a data block the value reads
		sev             Severity
		sensitive       bool
	}{
		{"ssm", `"{{resolve:ssm:/app/host}}"`, `data.aws_ssm_parameter.host.insecure_value`, `name = "/app/host"`, -1, false},
		{"ssm-secure with a version", `"{{resolve:ssm-secure:/app/db-pass:3}}"`, `data.aws_ssm_parameter.db_pass.value`, `name = "/app/db-pass:3"`, -1, true},
		{"secret's JSON key in text", `"postgres://{{resolve:secretsmanager:prod/db:SecretString:username}}@host"`,
			`"postgres://${jsondecode(data.aws_secretsmanager_secret_version.db.secret_string)["username"]}@host"`, `secret_id = "prod/db"`, -1, true},
		{"secret ARN and version stage", `"{{resolve:secretsmanager:arn:aws:secretsmanager:eu-west-2:123456789012:secret:app-AbCdEf:SecretString::AWSPREVIOUS}}"`,
			`data.aws_secretsmanager_secret_version.app_ab_cd_ef.secret_string`, `version_stage = "AWSPREVIOUS"`, -1, true},
		{"in Fn::Sub", `!Sub "https://{{resolve:ssm:/app/host}}/${Env}"`, `"https://${data.aws_ssm_parameter.host.insecure_value}/${var.env}"`, `name = "/app/host"`, -1, false},
		{"another service", `"{{resolve:s3:bucket/key}}"`, `null`, "", Error, false},
		{"a binary secret", `"{{resolve:secretsmanager:x:SecretBinary}}"`, `null`, "", Error, false},
		{"stage and version ID", `"{{resolve:secretsmanager:x:SecretString::AWSCURRENT:v1}}"`, `null`, "", Error, false},
	}
	tmpl := mustParse(t, exprTemplate)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ConvertValue(tmpl, mustNode(t, tt.src), "Test", Options{})
			if err != nil {
				t.Fatal(err)
			}
			if v.Expr != tt.want {
				t.Errorf("Expr = %s, want %s", v.Expr, tt.want)
			}
			if w := worst(v.Diagnostics); w != tt.sev {
				t.Errorf("diagnostics: %v", v.Diagnostics)
			}
			if v.Sensitive != tt.sensitive {
				t.Errorf("Sensitive = %v", v.Sensitive)
			}
			if tt.data != "" && !strings.Contains(squash([]byte(strings.Join(v.Data, "\n"))), tt.data) {
				t.Errorf("Data = %q, want %s", v.Data, tt.data)
			}
			if strings.Contains(v.Expr, "{{resolve:") {
				t.Errorf("the reference's text is left: %s", v.Expr)
			}
			if !exprParses(v.Expr) {
				t.Errorf("%s doesn't parse", v.Expr)
			}
		})
	}
}

// A $ right before an interpolation stays a $, and doesn't make HCL's
// $${ escape.
func TestDollarBeforeInterpolation(t *testing.T) {
	tests := []struct{ src, want string }{
		{`!Sub "cost $${Env}"`, `"cost ${"$"}${var.env}"`},
		{`!Sub "$$${Env}-x"`, `"${"$$"}${var.env}-x"`},
		{`!Sub ["cost $${A}", {A: "5"}]`, `"cost $5"`},
		{"Fn::Base64:\n  Fn::Sub: |\n    a $${Env}\n    b\n", "base64encode(<<EOT\na ${\"$\"}${var.env}\nb\nEOT\n)"},
	}
	for _, tt := range tests {
		got, _ := convertExpr(t, tt.src)
		if got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.src, got, tt.want)
		}
		if !exprParses(got) {
			t.Errorf("%s doesn't parse", got)
		}
	}
}

// A key whose value is AWS::NoValue is left out of a map written as it is,
// such as a policy document, and one that may be is filtered out.
func TestNoValueInMaps(t *testing.T) {
	got, _ := convertExpr(t, `!ToJsonString {a: 1, b: !Ref AWS::NoValue, c: !If [IsProd, 2, !Ref AWS::NoValue]}`)
	want := "jsonencode({ for k, v in {\na = 1\nc = local.is_prod ? 2 : null\n} : k => v if v != null })"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestReviewedConversions(t *testing.T) {
	res := mustConvert(t, `
Parameters:
  Secret: {Type: String, NoEcho: true}
  Version: {Type: String}
  Count: {Type: Number, Default: "+5"}
  Ratio: {Type: Number, Default: .5}
  Bad: {Type: Number, Default: Inf}
Conditions:
  HasSecret: !Not [!Equals [!Ref Secret, ""]]
  Open: !Equals [!Ref Version, "1"]
Resources:
  Group:
    Type: AWS::EC2::SecurityGroup
    Condition: HasSecret
    Properties:
      GroupDescription: !Ref Version
      SecurityGroupIngress:
        - IpProtocol: tcp
          FromPort: 443
          ToPort: 443
          CidrIp: !If [Open, 10.0.0.0/8, !Ref AWS::NoValue]
`, Options{})
	locals := squash(res.Files["locals.tf"])
	if !strings.Contains(locals, `has_secret = nonsensitive(!(var.secret == ""))`) {
		t.Errorf("locals.tf:\n%s", locals)
	}
	main := squash(res.Files["main.tf"])
	for _, want := range []string{`count = local.has_secret ? 1 : 0`, `cidr_blocks = local.open ? ["10.0.0.0/8"] : null`, `description = var.version_value`} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf lacks %s:\n%s", want, main)
		}
	}
	vars := squash(res.Files["variables.tf"])
	for _, want := range []string{`variable "version_value"`, `default = 5`, `default = 0.5`} {
		if !strings.Contains(vars, want) {
			t.Errorf("variables.tf lacks %s:\n%s", want, vars)
		}
	}
	if strings.Contains(vars, "Inf") && strings.Contains(vars, "default = \"Inf\"") {
		t.Errorf("variables.tf has a default Terraform can't read:\n%s", vars)
	}
	if !hasDiagnostic(res, Error, "Parameters.Bad.Default", "isn't a number") {
		t.Errorf("no error for the default: %v", res.Diagnostics)
	}
}

// sensitiveMapper is the default mapper, with every attribute sensitive.
type sensitiveMapper struct{ Mapper }

func (m sensitiveMapper) Attribute(cfnType, attr string) (Mapped, bool) {
	a, ok := m.Mapper.Attribute(cfnType, attr)
	a.Sensitive = true
	return a, ok
}

// An output of an attribute the mapper marks sensitive is sensitive, or
// Terraform refuses the plan.
func TestSensitiveAttributeOutput(t *testing.T) {
	res := mustConvert(t, `
Resources:
  P: {Type: AWS::SSM::Parameter, Properties: {Type: String, Value: x}}
Outputs:
  Value: {Value: !GetAtt P.Value}
`, Options{Mapper: sensitiveMapper{DefaultMapper()}})
	out := squash(res.Files["outputs.tf"])
	if !strings.Contains(out, "value = aws_ssm_parameter.p.value") || !strings.Contains(out, "sensitive = true") {
		t.Errorf("outputs.tf:\n%s", out)
	}
}

// A YAML alias of a long string, repeated, is kept in a comment only up to
// keptLimit, and the parse stops past maxBytes of text.
func TestAliasedValuesAreBounded(t *testing.T) {
	big := strings.Repeat("x", 512<<10)
	template := func(n int) string {
		var b strings.Builder
		b.WriteString("Metadata:\n  Big: &big \"" + big + "\"\nResources:\n  Hook:\n    Type: Custom::Hook\n    Properties:\n      Rows:\n")
		for i := 0; i < n; i++ {
			b.WriteString("        - *big\n")
		}
		return b.String()
	}
	res := mustConvert(t, template(100), Options{})
	if n := len(res.Files["main.tf"]); n > 1<<20 {
		t.Errorf("main.tf is %d bytes", n)
	}
	if !hasDiagnostic(res, Info, "", "cut at 64 KiB") {
		t.Errorf("no diagnostic for the cut value: %v", res.Diagnostics)
	}
	if _, err := Parse(strings.NewReader(template(200))); err == nil || !strings.Contains(err.Error(), "MiB of text") {
		t.Errorf("err = %v, want the text budget's", err)
	}
	if _, err := Parse(strings.NewReader("Resources: {}\n" + strings.Repeat(" ", maxInput))); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want the input cap's", err)
	}
}

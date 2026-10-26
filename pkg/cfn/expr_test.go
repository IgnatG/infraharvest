// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"strings"
	"testing"
)

// exprTemplate is the template the expression tests convert in: it has a
// parameter of each kind, a mapping, a condition and resources to refer to.
const exprTemplate = `
Parameters:
  Env:
    Type: String
  Count:
    Type: Number
  Subnets:
    Type: List<AWS::EC2::Subnet::Id>
  Secret:
    Type: String
    NoEcho: true
  AmiParam:
    Type: AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>
  NamesParam:
    Type: AWS::SSM::Parameter::Value<List<String>>
Mappings:
  RegionMap:
    us-east-1:
      Ami: ami-1
Conditions:
  IsProd: !Equals [!Ref Env, prod]
Resources:
  Vpc:
    Type: AWS::EC2::VPC
  Bucket:
    Type: AWS::S3::Bucket
    Condition: IsProd
  Db:
    Type: AWS::RDS::DBInstance
  Thing:
    Type: AWS::Foo::Bar
  Hook:
    Type: Custom::Hook
`

// convertExpr converts one value, given as YAML, in exprTemplate.
func convertExpr(t *testing.T, src string) (string, *converter) {
	t.Helper()
	c := newConverter(mustParse(t, exprTemplate), Options{})
	return c.expr(mustNode(t, src), "Test", nil), c
}

// worst returns the most severe diagnostic's severity, or -1 for none.
func worst(diags []Diagnostic) Severity {
	w := Severity(-1)
	for _, d := range diags {
		if d.Severity > w {
			w = d.Severity
		}
	}
	return w
}

func TestExpressions(t *testing.T) {
	none := Severity(-1)
	tests := []struct {
		name, src, want string
		sev             Severity // the most severe diagnostic, or none
	}{
		// Literals.
		{"string", `"a\"b\\c"`, `"a\"b\\c"`, none},
		{"template sequences", `"${x} %{y}"`, `"$${x} %%{y}"`, none},
		{"newline", `"a\nb"`, `"a\nb"`, none},
		{"number", `1.5`, `1.5`, none},
		{"YAML number HCL can't read", `0x1F`, `"0x1F"`, none},
		{"bool", `false`, `false`, none},
		{"null", `~`, `null`, none},
		{"list", `[a, 1]`, `["a", 1]`, none},
		{"map keys as written", `{a: 1, b-c: 2, "null": 3}`, "{\na = 1\n\"b-c\" = 2\n\"null\" = 3\n}", none},

		// Ref.
		{"Ref parameter", `!Ref Env`, `var.env`, none},
		{"Ref resource", `!Ref Vpc`, `aws_vpc.vpc.id`, none},
		{"Ref conditional resource", `!Ref Bucket`, `aws_s3_bucket.bucket[0].id`, none},
		{"Ref with another attribute", `!Ref Db`, `aws_db_instance.db.identifier`, none},
		{"Ref guessed", `!Ref Thing`, `aws_foo_bar.thing.id`, Info},
		{"Ref SSM parameter", `!Ref AmiParam`, `data.aws_ssm_parameter.ami_param.insecure_value`, none},
		{"Ref SSM list parameter", `!Ref NamesParam`, `split(",", data.aws_ssm_parameter.names_param.insecure_value)`, none},
		{"Ref custom resource", `!Ref Hook`, `null`, Error},
		{"Ref unknown", `!Ref Nope`, `null`, Error},
		{"Ref not a name", `{Ref: [a]}`, `null`, Error},

		// Pseudo parameters.
		{"AWS::Region", `!Ref AWS::Region`, `data.aws_region.current.region`, none},
		{"AWS::AccountId", `!Ref AWS::AccountId`, `data.aws_caller_identity.current.account_id`, none},
		{"AWS::Partition", `!Ref AWS::Partition`, `data.aws_partition.current.partition`, none},
		{"AWS::URLSuffix", `!Ref AWS::URLSuffix`, `data.aws_partition.current.dns_suffix`, none},
		{"AWS::StackName", `!Ref AWS::StackName`, `var.stack_name`, Info},
		{"AWS::StackId", `!Ref AWS::StackId`, `var.stack_id`, Info},
		{"AWS::NotificationARNs", `!Ref AWS::NotificationARNs`, `var.notification_arns`, Info},
		{"AWS::NoValue", `!Ref AWS::NoValue`, `null`, none},
		{"unknown pseudo parameter", `!Ref AWS::Nope`, `null`, Error},

		// Fn::GetAtt.
		{"GetAtt dotted", `!GetAtt Db.Endpoint.Address`, `aws_db_instance.db.address`, none},
		{"GetAtt list", `!GetAtt [Bucket, Arn]`, `aws_s3_bucket.bucket[0].arn`, none},
		{"GetAtt guessed", `!GetAtt Thing.SomeAttr`, `aws_foo_bar.thing.some_attr`, Info},
		{"GetAtt computed attribute", `!GetAtt [Vpc, !Ref Env]`, `null`, Error},
		{"GetAtt custom resource", `!GetAtt Hook.Out`, `null`, Error},
		{"GetAtt unknown", `!GetAtt Nope.Arn`, `null`, Error},

		// Fn::Sub.
		{"Sub", `!Sub "${Env}-x"`, `"${var.env}-x"`, none},
		{"Sub one variable", `!Sub "${AWS::Region}"`, `data.aws_region.current.region`, none},
		{"Sub literal", `!Sub "${!Literal}-${Env}"`, `"$${Literal}-${var.env}"`, none},
		{"Sub attribute", `!Sub "${Vpc.CidrBlock}/x"`, `"${aws_vpc.vpc.cidr_block}/x"`, none},
		{"Sub variables", `!Sub ["${A}-${Env}", {A: !Ref Vpc}]`, `"${aws_vpc.vpc.id}-${var.env}"`, none},
		{"Sub percent", `!Sub "100%{x}"`, `"100%%{x}"`, none},
		{"Sub unclosed", `!Sub "${Env"`, `"$${Env"`, none},
		{"Sub no heredoc inside", `!Sub "a\n${Env}\n"`, `"a\n${var.env}\n"`, none},
		{"Sub bad", `!Sub [a, b, c]`, `null`, Error},

		// Other functions.
		{"Join", `!Join [",", [a, !Ref Env]]`, `join(",", ["a", var.env])`, none},
		{"Join list parameter", `!Join [",", !Ref Subnets]`, `join(",", var.subnets)`, none},
		{"Select", `!Select [0, !GetAZs ""]`, `element(data.aws_availability_zones.available.names, 0)`, none},
		{"Select string index", `!Select ["1", !Ref Subnets]`, `element(var.subnets, 1)`, none},
		{"Split", `!Split [",", "a,b"]`, `split(",", "a,b")`, none},
		{"Base64", `!Base64 abc`, `base64encode("abc")`, none},
		{"Base64 heredoc", "!Base64 |\n  #!/bin/sh\n  echo hi\n", "base64encode(<<EOT\n#!/bin/sh\necho hi\nEOT\n)", none},
		{"Base64 Sub heredoc", "!Base64\n  Fn::Sub: |\n    echo ${Env}\n    echo ${!x}\n", "base64encode(<<EOT\necho ${var.env}\necho $${x}\nEOT\n)", none},
		{"Cidr literal", `!Cidr ["10.0.0.0/16", 3, 8]`, `cidrsubnets("10.0.0.0/16", 8, 8, 8)`, none},
		{"Cidr many", `!Cidr ["10.0.0.0/16", 20, 8]`, `[for i in range(20) : cidrsubnet("10.0.0.0/16", 8, i)]`, none},
		{"Cidr IPv6", `!Cidr ["2001:db8::/56", 2, 64]`, `cidrsubnets("2001:db8::/56", 8, 8)`, none},
		{"Cidr expression", `!Cidr [!GetAtt Vpc.CidrBlock, 2, 8]`,
			`[for i in range(2) : cidrsubnet(aws_vpc.vpc.cidr_block, (strcontains(aws_vpc.vpc.cidr_block, ":") ? 128 : 32) - 8 - tonumber(split("/", aws_vpc.vpc.cidr_block)[1]), i)]`, none},
		{"GetAZs region", "Fn::GetAZs: !Ref AWS::Region", `data.aws_availability_zones.available.names`, none},
		{"GetAZs other region", `!GetAZs eu-west-1`, `data.aws_availability_zones.available.names`, Warning},
		{"If", `!If [IsProd, a, b]`, `local.is_prod ? "a" : "b"`, none},
		{"If nested", `!If [IsProd, !If [IsProd, a, b], c]`, `local.is_prod ? (local.is_prod ? "a" : "b") : "c"`, none},
		{"If NoValue", `!If [IsProd, a, !Ref AWS::NoValue]`, `local.is_prod ? "a" : null`, none},
		{"If unknown condition", `!If [Nope, a, b]`, `false ? "a" : "b"`, Error},
		{"If wrong arity", `!If [IsProd, a]`, `null`, Error},
		{"list NoValue", `[a, !If [IsProd, b, !Ref AWS::NoValue]]`, `[for v in ["a", local.is_prod ? "b" : null] : v if v != null]`, none},
		{"FindInMap", `!FindInMap [RegionMap, !Ref AWS::Region, Ami]`, `local.region_map[data.aws_region.current.region]["Ami"]`, none},
		{"FindInMap default", `!FindInMap [RegionMap, a, b, {DefaultValue: c}]`, `try(local.region_map["a"]["b"], "c")`, none},
		{"FindInMap unknown", `!FindInMap [Nope, a, b]`, `null`, Error},
		{"ImportValue", `!ImportValue Shared-VpcId`, `var.import_shared_vpc_id`, Warning},
		{"ImportValue Sub", "Fn::ImportValue: !Sub \"${Env}-VpcId\"", `var.import_env_vpc_id`, Warning},
		{"Length", `!Length [a, b]`, `length(["a", "b"])`, none},
		{"ToJsonString", `!ToJsonString {a: 1}`, "jsonencode({\na = 1\n})", none},
		{"Transform", `!Transform {Name: AWS::Include}`, `null`, Error},
		{"unknown function", `!Bogus x`, `null`, Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, c := convertExpr(t, tt.src)
			if got != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
			if w := worst(c.diags); w != tt.sev {
				t.Errorf("worst diagnostic %v, want %v: %v", w, tt.sev, c.diags)
			}
			if !exprParses(got) {
				t.Errorf("%s doesn't parse as HCL", got)
			}
		})
	}
}

func TestConditions(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"Equals", `!Equals [!Ref Env, prod]`, `var.env == "prod"`},
		{"Equals number parameter", `!Equals [!Ref Count, "3"]`, `var.count == 3`},
		{"Equals number literal with a string", `!Equals [!Ref Env, 3]`, `var.env == "3"`},
		{"Equals bool literal", `!Equals [!Ref Env, true]`, `var.env == "true"`},
		{"And", `!And [!Condition IsProd, !Equals [a, b]]`, `local.is_prod && ("a" == "b")`},
		{"Or", `!Or [!Condition IsProd, !Not [!Condition IsProd], !Equals [a, b]]`, `local.is_prod || (!local.is_prod) || ("a" == "b")`},
		{"Not", `!Not [!Condition IsProd]`, `!local.is_prod`},
		{"Not Equals", `!Not [!Equals [!Ref Env, x]]`, `!(var.env == "x")`},
		{"JSON Condition", `{"Fn::And": [{"Condition": "IsProd"}, {"Condition": "IsProd"}]}`, `local.is_prod && local.is_prod`},
		{"Condition map", `{"Condition": "IsProd"}`, `local.is_prod`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newConverter(mustParse(t, exprTemplate), Options{})
			got := c.condition(mustNode(t, tt.src), "Test")
			if got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
			if len(c.diags) > 0 {
				t.Errorf("diagnostics: %v", c.diags)
			}
			if !exprParses(got) {
				t.Errorf("%s doesn't parse as HCL", got)
			}
		})
	}
}

func TestParseSub(t *testing.T) {
	tests := []struct {
		in   string
		want []subPart
	}{
		{"", nil},
		{"plain", []subPart{{text: "plain"}}},
		{"${A}", []subPart{{text: "A", isVar: true}}},
		{"a${ B }c", []subPart{{text: "a"}, {text: "B", isVar: true}, {text: "c"}}},
		{"${!A}${B.C}", []subPart{{text: "${A}"}, {text: "B.C", isVar: true}}},
		{"${}", []subPart{{text: "${}"}}},
		{"x${", []subPart{{text: "x${"}}},
	}
	for _, tt := range tests {
		got := parseSub(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("%q: got %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%q: got %v, want %v", tt.in, got, tt.want)
				break
			}
		}
	}
}

func TestSnake(t *testing.T) {
	tests := map[string]string{
		"VPCGatewayAttachment": "vpc_gateway_attachment",
		"MyS3Bucket":           "my_s3_bucket",
		"DBInstance":           "db_instance",
		"Ipv6CidrBlock":        "ipv6_cidr_block",
		"IPv6CidrBlock":        "ipv6_cidr_block",
		"HTTPSListener":        "https_listener",
		"Subnet1":              "subnet1",
		"AZ1Subnet":            "az1_subnet",
		"us-east-1":            "us_east_1",
		"already_snake":        "already_snake",
		"__X__":                "x",
	}
	for in, want := range tests {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ident("1st"); got != "_1st" {
		t.Errorf("ident(1st) = %q", got)
	}
}

// What the converter can't convert is kept, as written, in a note.
func TestNotesKeepWhatIsLeftOut(t *testing.T) {
	_, c := convertExpr(t, `!Bogus {a: 1}`)
	joined := strings.Join(c.notes, "\n")
	if !strings.Contains(joined, `It was: {"Fn::Bogus":{"a":1}}`) {
		t.Errorf("notes = %q", joined)
	}
}

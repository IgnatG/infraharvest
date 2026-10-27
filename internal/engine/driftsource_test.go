// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// testEditor is a driftEditor for the resources at their addresses, each
// with the cloud's values after.
func testEditor(after map[string]map[string]any) *driftEditor {
	e := &driftEditor{resources: map[string]refreshedResource{}, refs: map[string][]referenceTarget{}}
	for address, values := range after {
		e.resources[address] = refreshedResource{after: values}
	}
	return e
}

// driftAt is a change, the next apply would revert, to the attribute of
// the resource at address, whose new value in the cloud is value.
func driftAt(address, attribute string, value any) *DriftChange {
	typ, _, _ := strings.Cut(address, ".")
	return &DriftChange{Address: address, Type: typ, Attribute: attribute, Kind: DriftChanged, Outcome: DriftReverts, value: value}
}

func byteFiles(files map[string]string) map[string][]byte {
	out := map[string][]byte{}
	for name, content := range files {
		out[name] = []byte(content)
	}
	return out
}

const sharedTags = `locals {
  tags = {
    Env = "prod"
  }
}
`

func TestDriftSourceMapping(t *testing.T) {
	t.Parallel()
	listener := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_lb_listener": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"port": {AttributeType: cty.Number}, "protocol": {AttributeType: cty.String}}}},
	}}}}
	for _, tc := range []struct {
		name   string
		files  map[string]string
		after  map[string]any
		change *DriftChange
		setup  func(e *driftEditor)
		kind   string
		// want are the files the edit changes, as it leaves them; none
		// means there is no edit, for reason.
		want   map[string]string
		reason string
		secret bool
	}{
		{
			name: "a key of a map literal",
			files: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}
`},
			after:  map[string]any{"bucket": "logs", "tags": map[string]any{"Env": "dev"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Env", "dev"),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "dev"
  }
}
`},
		},
		{
			name: "a key the map literal doesn't have",
			files: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}
`},
			after:  map[string]any{"tags": map[string]any{"Env": "prod", "Team": "data"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Team", "data"),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env  = "prod"
    Team = "data"
  }
}
`},
		},
		{
			name: "a key the cloud removed",
			files: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env  = "prod"
    Team = "data"
  }
}
`},
			after:  map[string]any{"tags": map[string]any{"Env": "prod"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Team", nil),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}
`},
		},
		{
			name: "an argument the code leaves out, where the layout puts it",
			files: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}
`},
			after:  map[string]any{"bucket": "logs", "force_destroy": true},
			change: driftAt("aws_s3_bucket.logs", "force_destroy", true),
			kind:   SourceAbsent,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket        = "logs"
  force_destroy = true
  tags = {
    Env = "prod"
  }
}
`},
		},
		{
			name: "a tag the code has no tags for",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  name = "jobs"
}
`},
			after:  map[string]any{"name": "jobs", "tags": map[string]any{"Env": "dev"}},
			change: driftAt("aws_sqs_queue.jobs", "tags.Env", "dev"),
			kind:   SourceAbsent,
			want: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  name = "jobs"
  tags = {
    Env = "dev"
  }
}
`},
		},
		{
			name: "an argument of a nested block",
			files: map[string]string{"main.tf": `resource "aws_instance" "web" {
  ami = "ami-1"

  root_block_device {
    volume_size = 8
  }
}
`},
			after:  map[string]any{"ami": "ami-1", "root_block_device": []any{map[string]any{"volume_size": float64(20)}}},
			change: driftAt("aws_instance.web", "root_block_device[0].volume_size", float64(20)),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_instance" "web" {
  ami = "ami-1"

  root_block_device {
    volume_size = 20
  }
}
`},
		},
		{
			name: "a list, written whole",
			files: map[string]string{"main.tf": `resource "aws_security_group_rule" "web" {
  cidr_blocks = ["10.0.0.0/8", "10.1.0.0/16"]
}
`},
			after:  map[string]any{"cidr_blocks": []any{"0.0.0.0/0", "10.1.0.0/16"}},
			change: driftAt("aws_security_group_rule.web", "cidr_blocks[0]", "0.0.0.0/0"),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_security_group_rule" "web" {
  cidr_blocks = ["0.0.0.0/0", "10.1.0.0/16"]
}
`},
		},
		{
			name: "a number, typed as the schema says",
			files: map[string]string{"main.tf": `resource "aws_lb_listener" "web" {
  port     = 443
  protocol = "HTTPS"
}
`},
			after:  map[string]any{"port": "80", "protocol": float64(80)},
			change: driftAt("aws_lb_listener.web", "port", "80"),
			setup:  func(e *driftEditor) { e.schemas = listener },
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_lb_listener" "web" {
  port     = 80
  protocol = "HTTPS"
}
`},
		},
		{
			name: "a string, typed as the schema says",
			files: map[string]string{"main.tf": `resource "aws_lb_listener" "web" {
  port     = 443
  protocol = "HTTPS"
}
`},
			after:  map[string]any{"protocol": float64(80)},
			change: driftAt("aws_lb_listener.web", "protocol", float64(80)),
			setup:  func(e *driftEditor) { e.schemas = listener },
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_lb_listener" "web" {
  port     = 443
  protocol = "80"
}
`},
		},
		{
			name: "the catalogue's default, left out",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  name          = "jobs"
  delay_seconds = 30
}
`},
			after:  map[string]any{"name": "jobs", "delay_seconds": float64(0)},
			change: driftAt("aws_sqs_queue.jobs", "delay_seconds", float64(0)),
			setup:  func(e *driftEditor) { e.defaults = map[string][]string{"aws_sqs_queue": {"delay_seconds=0"}} },
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  name = "jobs"
}
`},
		},
		{
			name: "a local only this resource uses",
			files: map[string]string{
				"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = local.logs_tags
}
`,
				"locals.tf": `locals {
  logs_tags = {
    Env = "prod"
  }
}
`,
			},
			after:  map[string]any{"tags": map[string]any{"Env": "dev"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Env", "dev"),
			kind:   SourceLocal,
			want: map[string]string{"locals.tf": `locals {
  logs_tags = {
    Env = "dev"
  }
}
`},
		},
		{
			name: "a key of a local other resources use",
			files: map[string]string{
				"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = local.tags
}

resource "aws_s3_bucket" "data" {
  bucket = "data"
  tags   = local.tags
}
`,
				"locals.tf": sharedTags,
			},
			after:  map[string]any{"tags": map[string]any{"Env": "dev"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Env", "dev"),
			kind:   SourceSharedLocal,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = merge(local.tags, { Env = "dev" })
}

resource "aws_s3_bucket" "data" {
  bucket = "data"
  tags   = local.tags
}
`},
		},
		{
			name: "a list local other resources use",
			files: map[string]string{
				"main.tf": `resource "aws_security_group_rule" "a" {
  cidr_blocks = local.cidrs
}

resource "aws_security_group_rule" "b" {
  cidr_blocks = local.cidrs
}
`,
				"locals.tf": `locals {
  cidrs = ["10.0.0.0/8"]
}
`,
			},
			after:  map[string]any{"cidr_blocks": []any{"0.0.0.0/0"}},
			change: driftAt("aws_security_group_rule.a", "cidr_blocks[0]", "0.0.0.0/0"),
			kind:   SourceSharedLocal,
			want: map[string]string{"main.tf": `resource "aws_security_group_rule" "a" {
  cidr_blocks = ["0.0.0.0/0"]
}

resource "aws_security_group_rule" "b" {
  cidr_blocks = local.cidrs
}
`},
		},
		{
			name: "merge, with the resource's own map",
			files: map[string]string{
				"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = merge(local.tags, { Team = "data" })
}
`,
				"locals.tf": sharedTags,
			},
			after:  map[string]any{"tags": map[string]any{"Env": "dev", "Team": "data"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Env", "dev"),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = merge(local.tags, { Team = "data", Env = "dev" })
}
`},
		},
		{
			name: "merge, without a map of the resource's own",
			files: map[string]string{
				"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = merge(local.tags, local.extra)
}
`,
			},
			after:  map[string]any{"tags": map[string]any{"Env": "dev"}},
			change: driftAt("aws_s3_bucket.logs", "tags.Env", "dev"),
			kind:   SourceLiteral,
			want: map[string]string{"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = merge(local.tags, local.extra, { Env = "dev" })
}
`},
		},
		{
			name: "a reference, to another managed resource",
			files: map[string]string{"main.tf": `resource "aws_subnet" "a" {
  vpc_id = aws_vpc.main.id
}
`},
			after:  map[string]any{"vpc_id": "vpc-0bbb2222"},
			change: driftAt("aws_subnet.a", "vpc_id", "vpc-0bbb2222"),
			setup: func(e *driftEditor) {
				e.refs = referenceIndex(map[string]map[string]any{"aws_vpc.main": {"id": "vpc-0aaa1111"}, "aws_vpc.other": {"id": "vpc-0bbb2222"}})
			},
			kind: SourceReference,
			want: map[string]string{"main.tf": `resource "aws_subnet" "a" {
  vpc_id = aws_vpc.other.id
}
`},
		},
		{
			name: "a reference, to what isn't managed",
			files: map[string]string{"main.tf": `resource "aws_subnet" "a" {
  vpc_id = aws_vpc.main.id
}
`},
			after:  map[string]any{"vpc_id": "vpc-0ccc3333"},
			change: driftAt("aws_subnet.a", "vpc_id", "vpc-0ccc3333"),
			kind:   SourceReference,
			reason: "refers to aws_vpc.main.id",
		},
		{
			name: "a variable with another root's value",
			files: map[string]string{
				"main.tf": `resource "aws_subnet" "a" {
  vpc_id = var.vpc_id
}
`,
				"variables.tf": `variable "vpc_id" {
  type    = string
  default = "vpc-0aaa1111"
}
`,
			},
			after:  map[string]any{"vpc_id": "vpc-0bbb2222"},
			change: driftAt("aws_subnet.a", "vpc_id", "vpc-0bbb2222"),
			kind:   SourceVariable,
			reason: "update it from that root",
		},
		{
			name: "a sensitive variable",
			files: map[string]string{
				"main.tf": `resource "aws_db_instance" "db" {
  password = var.db_password
}
`,
				"variables.tf": `variable "db_password" {
  type      = string
  sensitive = true
}
`,
			},
			after:  map[string]any{"password": "x"},
			change: driftAt("aws_db_instance.db", "password", "x"),
			kind:   SourceSensitiveVariable,
			reason: "TF_VAR_db_password",
			secret: true,
		},
		{
			name: "a function",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  name = lower("JOBS")
}
`},
			after:  map[string]any{"name": "work"},
			change: driftAt("aws_sqs_queue.jobs", "name", "work"),
			kind:   SourceExpression,
			reason: "lower() computes it",
		},
		{
			name: "a conditional",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  name = var.prefix == "" ? "jobs" : "other"
}
`},
			after:  map[string]any{"name": "work"},
			change: driftAt("aws_sqs_queue.jobs", "name", "work"),
			kind:   SourceExpression,
			reason: "edit it by hand",
		},
		{
			name: "a for expression",
			files: map[string]string{"main.tf": `resource "aws_security_group_rule" "web" {
  cidr_blocks = [for c in var.cidrs : c]
}
`},
			after:  map[string]any{"cidr_blocks": []any{"0.0.0.0/0"}},
			change: driftAt("aws_security_group_rule.web", "cidr_blocks[0]", "0.0.0.0/0"),
			kind:   SourceExpression,
			reason: "edit it by hand",
		},
		{
			name: "a dynamic block",
			files: map[string]string{"main.tf": `resource "aws_security_group" "web" {
  name = "web"

  dynamic "ingress" {
    for_each = var.ports
    content {
      from_port = ingress.value
    }
  }
}
`},
			after:  map[string]any{"ingress": []any{map[string]any{"from_port": float64(80)}}},
			change: driftAt("aws_security_group.web", "ingress[0].from_port", float64(80)),
			kind:   SourceExpression,
			reason: "a dynamic ingress block",
		},
		{
			name: "a block with count",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  count = 2
  name  = "jobs"
}
`},
			after:  map[string]any{"name": "work"},
			change: driftAt("aws_sqs_queue.jobs[0]", "name", "work"),
			kind:   SourceExpression,
			reason: "count",
		},
		{
			name:   "a resource in a module call",
			files:  map[string]string{"main.tf": "module \"data\" {\n  source = \"./data\"\n}\n"},
			after:  map[string]any{"tags": map[string]any{"Team": "b"}},
			change: &DriftChange{Address: "module.data.aws_s3_bucket.this", Type: "aws_s3_bucket", ModuleAddress: "module.data", Attribute: "tags.Team", Kind: DriftChanged, Outcome: DriftReverts, value: "b"},
			kind:   SourceModule,
			reason: "module.data",
		},
		{
			name: "a value the plan marks sensitive",
			files: map[string]string{"main.tf": `resource "aws_ssm_parameter" "p" {
  value = "old"
}
`},
			after: map[string]any{"value": "rotated-secret-value"},
			change: func() *DriftChange {
				c := driftAt("aws_ssm_parameter.p", "value", "rotated-secret-value")
				c.sensitive = true
				return c
			}(),
			kind:   SourceLiteral,
			reason: "never written",
			secret: true,
		},
		{
			name: "a credential",
			files: map[string]string{"main.tf": `resource "aws_lambda_function" "api" {
  environment {
    variables = {
      LOG_LEVEL = "debug"
    }
  }
}
`},
			after:  map[string]any{"environment": []any{map[string]any{"variables": map[string]any{"LOG_LEVEL": fuzzToken("credential")}}}},
			change: driftAt("aws_lambda_function.api", "environment[0].variables.LOG_LEVEL", fuzzToken("credential")),
			kind:   SourceLiteral,
			reason: "never written",
			secret: true,
		},
		{
			name: "a secret by its name",
			files: map[string]string{"main.tf": `resource "aws_lambda_function" "api" {
  environment {
    variables = {
      LOG_LEVEL = "debug"
    }
  }
}
`},
			after:  map[string]any{"environment": []any{map[string]any{"variables": map[string]any{"LOG_LEVEL": "debug", "WEBHOOK_SECRET": fakeSecret("webhook")}}}},
			change: driftAt("aws_lambda_function.api", "environment[0].variables.WEBHOOK_SECRET", fakeSecret("webhook")),
			kind:   SourceLiteral,
			reason: "never written",
			secret: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := testEditor(map[string]map[string]any{tc.change.Address: tc.after})
			if tc.setup != nil {
				tc.setup(e)
			}
			contents := byteFiles(tc.files)
			before := maps.Clone(contents)
			names, applied, failed, err := e.apply(contents, []*DriftChange{tc.change})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				src, ok := failed[tc.change]
				if !ok {
					t.Fatalf("edited %v as %+v, want no edit", names, applied[tc.change])
				}
				if src.Kind != tc.kind || !strings.Contains(src.Reason, tc.reason) || src.Secret != tc.secret {
					t.Errorf("source %s (secret %v): %q, want %s (secret %v) saying %q", src.Kind, src.Secret, src.Reason, tc.kind, tc.secret, tc.reason)
				}
				if !reflect.DeepEqual(contents, before) {
					t.Errorf("the files changed: %v", names)
				}
				return
			}
			if src, ok := failed[tc.change]; ok {
				t.Fatalf("no edit: %s: %s", src.Kind, src.Reason)
			}
			if got := applied[tc.change].Kind; got != tc.kind {
				t.Errorf("source %s, want %s", got, tc.kind)
			}
			for name, content := range contents {
				want, ok := tc.want[name]
				if !ok {
					want = tc.files[name]
				}
				if string(content) != want {
					t.Errorf("%s:\n%s\nwant:\n%s", name, content, want)
				}
			}
		})
	}
}

// Edits keep the comments and the layout of what they don't change, and
// two edits of one file are found each in the file as the other left it.
func TestDriftEditsKeepFormatting(t *testing.T) {
	t.Parallel()
	const before = `# Buckets for the logs.
resource "aws_s3_bucket" "logs" {
  bucket = "logs" # the name

  # Who to ask.
  tags = {
    Env  = "prod" # set by the platform team
    Team = "data"
  }
}

resource "aws_s3_bucket" "data" {
  bucket = "data"
  // kept as it is
  tags = { Env = "prod" }
}
`
	const want = `# Buckets for the logs.
resource "aws_s3_bucket" "logs" {
  bucket = "logs" # the name

  # Who to ask.
  tags = {
    Env  = "dev" # set by the platform team
    Team = "data"
  }
}

resource "aws_s3_bucket" "data" {
  bucket = "data"
  // kept as it is
  tags = { Env = "prod", Team = "data" }
}
`
	e := testEditor(map[string]map[string]any{
		"aws_s3_bucket.logs": {"tags": map[string]any{"Env": "dev", "Team": "data"}},
		"aws_s3_bucket.data": {"tags": map[string]any{"Env": "prod", "Team": "data"}},
	})
	contents := map[string][]byte{"main.tf": []byte(before)}
	changes := []*DriftChange{driftAt("aws_s3_bucket.logs", "tags.Env", "dev"), driftAt("aws_s3_bucket.data", "tags.Team", "data")}
	names, _, failed, err := e.apply(contents, changes)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) > 0 || !reflect.DeepEqual(names, []string{"main.tf"}) {
		t.Fatalf("edited %v, failed %v", names, failed)
	}
	if got := string(contents["main.tf"]); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSplitPathKeepsDottedKeys(t *testing.T) {
	t.Parallel()
	after := map[string]any{"tags": map[string]any{"kubernetes.io/role": "a"}, "block": []any{map[string]any{"x": 1}}}
	got := splitPath("tags.kubernetes.io/role", after)
	want := []pathStep{{name: "tags"}, {name: "kubernetes.io/role"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	got = splitPath("block[0].x", after)
	want = []pathStep{{name: "block"}, {index: 0, isIndex: true}, {name: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestForcesReplacement(t *testing.T) {
	t.Parallel()
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		driftResource("aws_instance.web", "aws_instance", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"subnet_id": "subnet-0aaa1111", "tags": map[string]any{"Env": "prod"}},
			map[string]any{"subnet_id": "subnet-0bbb2222", "tags": map[string]any{"Env": "dev"}}),
	}}
	replace := driftResource("aws_instance.web", "aws_instance", tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate},
		map[string]any{"subnet_id": "subnet-0bbb2222", "tags": map[string]any{"Env": "dev"}},
		map[string]any{"subnet_id": "subnet-0aaa1111", "tags": map[string]any{"Env": "prod"}})
	replace.Change.ReplacePaths = []any{[]any{"subnet_id"}}
	changes := classifyDrift(refresh, &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{replace}}, nil, false)
	got := map[string]bool{}
	for _, c := range changes {
		got[c.Attribute] = c.ForcesReplacement
	}
	if want := map[string]bool{"subnet_id": true, "tags.Env": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("forces replacement %v, want %v", got, want)
	}
	if !forcesReplacement(nil, "tags.Env") {
		t.Error("a replacement without its paths isn't taken for the attribute's")
	}
}

// An edit that removes one literal secret and adds another is refused,
// though the file holds as many secrets as before.
func TestEditFilesRefusesASecretInPlaceOfAnother(t *testing.T) {
	t.Parallel()
	token := `"ghp_` + strings.Repeat("a1", 18) + `"`
	file := func(a, b string) string {
		return "resource \"aws_vpc\" \"a\" {\n  tags = {\n    token = " + a + "\n  }\n}\n\nresource \"aws_vpc\" \"b\" {\n  tags = {\n    token = " + b + "\n  }\n}\n"
	}
	old := file(token, `"none"`)
	edited := file(`"none"`, token)

	_, reason, secret := editFiles(map[string][]byte{"main.tf": []byte(old)}, []SourceEdit{{File: "main.tf", Start: 0, End: len(old), Text: []byte(edited)}})

	if !secret || reason == "" {
		t.Errorf("wrote a secret in place of another: %q, secret %v", reason, secret)
	}
}

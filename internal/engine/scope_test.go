// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// awsScope is what an AWS root of account 680077350850 reads of its own
// account and region, "" for a root of global services, as the importer
// fills it from the provider's profile.
func awsScope(region string) *ScopeLocals {
	s := &ScopeLocals{Account: ScopeValue{ID: "680077350850", Data: "aws_caller_identity.current", Local: "aws_account_id", Expr: "data.aws_caller_identity.current.account_id"}}
	if region != "" {
		s.Region = ScopeValue{ID: region, Data: "aws_region.current", Local: "aws_region", Expr: "data.aws_region.current.region"}
	}
	return s
}

const scopeGenerated = `resource "aws_lambda_permission" "proxy" {
  function_name = "arn:aws:lambda:eu-west-2:680077350850:function:terramend-proxy-fn"
  principal     = "sns.amazonaws.com"
}

resource "aws_cloudwatch_metric_alarm" "errors" {
  alarm_actions = ["arn:aws:sns:eu-west-2:680077350850:ih-sbx-terraform-events"]
  tags = {
    Account = "680077350850"
  }
}

resource "aws_iam_policy" "deploy" {
  policy = jsonencode({
    Statement = [{
      Resource  = "arn:aws:iam::680077350850:role/terramend-proxy-*"
      Condition = { StringEquals = { "aws:SourceAccount" = "680077350850", "680077350850" = "key" } }
    }]
  })
}

resource "aws_subnet" "a" {
  availability_zone = "eu-west-2a"
  owner_id          = "680077350850"
}
`

// lifted lifts scope in generated, as the annotation stage does once the
// references are made, and returns the workspace.
func lifted(t *testing.T, files map[string]string, scope *ScopeLocals, taken Names) *workspace {
	t.Helper()
	ws := memoryWorkspace(files)
	replan, err := annotate(ws, referencedAnnotations(ws, Options{Scope: scope, DefaultTags: &DefaultTags{Attribute: "tags"}}, taken))
	if err != nil {
		t.Fatal(err)
	}
	if len(replan) != 0 {
		t.Errorf("replan %v, want none: the values stay the same", replan)
	}
	return ws
}

// A regional root's own account and region read from locals of data
// sources, as a merged root's do: inside ARNs, and alone where they are
// the whole value. Tags, object keys and a zone that only starts with
// the region stay literal.
func TestLiftScopeReadsTheRootsAccountAndRegion(t *testing.T) {
	t.Parallel()
	ws := lifted(t, map[string]string{rootfile.GeneratedFileName: scopeGenerated}, awsScope("eu-west-2"), nil)

	got := generatedText(t, ws)
	for _, want := range []string{
		`function_name = "arn:aws:lambda:${local.aws_region}:${local.aws_account_id}:function:terramend-proxy-fn"`,
		`alarm_actions = ["arn:aws:sns:${local.aws_region}:${local.aws_account_id}:ih-sbx-terraform-events"]`,
		`Resource  = "arn:aws:iam::${local.aws_account_id}:role/terramend-proxy-*"`,
		`"aws:SourceAccount" = local.aws_account_id`,
		`"680077350850" = "key"`,
		`Account = "680077350850"`,
		`availability_zone = "eu-west-2a"`,
		`owner_id          = local.aws_account_id`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf doesn't have %s:\n%s", want, got)
		}
	}
	// As the layout writes it, which collapses the empty blocks.
	data := string(hclfile.CollapseEmptyBlocks([]byte(wsFile(t, ws, rootfile.DataFileName))))
	for _, want := range []string{`data "aws_caller_identity" "current" {}`, `data "aws_region" "current" {}`} {
		if !strings.Contains(data, want) {
			t.Errorf("data.tf doesn't have %s:\n%s", want, data)
		}
	}
	locals := strings.Join(strings.Fields(wsFile(t, ws, rootfile.LocalsFileName)), " ")
	for _, want := range []string{"aws_account_id = data.aws_caller_identity.current.account_id", "aws_region = data.aws_region.current.region"} {
		if !strings.Contains(locals, want) {
			t.Errorf("locals.tf doesn't have %s:\n%s", want, locals)
		}
	}
	sameValues(t, scopeGenerated, got, map[string]cty.Value{"aws_account_id": cty.StringVal("680077350850"), "aws_region": cty.StringVal("eu-west-2")})
}

// sameValues fails the test where an argument of after, with locals, has
// another value than it has in before: a plan reads the same.
func sameValues(t *testing.T, before, after string, locals map[string]cty.Value) {
	t.Helper()
	b, err := hclfile.Parse("before.tf", []byte(before))
	if err != nil {
		t.Fatal(err)
	}
	a, err := hclfile.Parse("after.tf", []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(locals)}, Functions: map[string]function.Function{"jsonencode": stdlib.JSONEncodeFunc}}
	var walk func(before, after *hclsyntax.Body, at string)
	walk = func(before, after *hclsyntax.Body, at string) {
		for name, attr := range before.Attributes {
			want, diags := attr.Expr.Value(ctx)
			if diags.HasErrors() {
				t.Fatalf("%s.%s: %s", at, name, diags)
			}
			other, ok := after.Attributes[name]
			if !ok {
				t.Errorf("%s.%s is gone", at, name)
				continue
			}
			got, diags := other.Expr.Value(ctx)
			if diags.HasErrors() {
				t.Errorf("%s.%s: %s", at, name, diags)
				continue
			}
			if !got.RawEquals(want) {
				t.Errorf("%s.%s is %#v, want %#v", at, name, got, want)
			}
		}
		for i, block := range before.Blocks {
			walk(block.Body, after.Blocks[i].Body, at+"."+strings.Join(block.Labels, "."))
		}
	}
	walk(b.Syntax, a.Syntax, "")
}

// A root of global services has no region of its own: its account reads
// from a local, and a region in its strings stays literal.
func TestLiftScopeLeavesTheRegionOfAGlobalRoot(t *testing.T) {
	t.Parallel()
	ws := lifted(t, map[string]string{rootfile.GeneratedFileName: scopeGenerated}, awsScope(""), nil)

	got := generatedText(t, ws)
	if want := `"arn:aws:lambda:eu-west-2:${local.aws_account_id}:function:terramend-proxy-fn"`; !strings.Contains(got, want) {
		t.Errorf("generated.tf doesn't have %s:\n%s", want, got)
	}
	if strings.Contains(got, "local.aws_region") || strings.Contains(wsFile(t, ws, rootfile.DataFileName), "aws_region") || strings.Contains(wsFile(t, ws, rootfile.LocalsFileName), "aws_region") {
		t.Errorf("the global root reads a region:\n%s", got)
	}
}

// A root with nothing to lift gets no data source and no local.
func TestLiftScopeAddsNothingWithoutTheScope(t *testing.T) {
	t.Parallel()
	const src = `resource "aws_sqs_queue" "orders" {
  name = "orders"
}
`
	ws := lifted(t, map[string]string{rootfile.GeneratedFileName: src}, awsScope("eu-west-2"), nil)

	if got := generatedText(t, ws); got != src {
		t.Errorf("generated.tf changed:\n%s", got)
	}
	for _, name := range []string{rootfile.DataFileName, rootfile.LocalsFileName} {
		if _, ok := ws.files[name]; ok {
			t.Errorf("%s written for nothing to lift", name)
		}
	}
}

// The literal locals the literal lift wrote read the scope too, and the
// locals.tf that holds them still takes literal locals: the local of a
// data source stays as it is.
func TestLiftScopeKeepsLocalsWritable(t *testing.T) {
	t.Parallel()
	ws := lifted(t, map[string]string{
		rootfile.GeneratedFileName: "resource \"aws_iam_role_policy_attachment\" \"a\" {\n  role = local.role_arn\n}\n",
		rootfile.LocalsFileName:    "locals {\n  role_arn = \"arn:aws:iam::680077350850:role/app\"\n  tags = {\n    Account = \"680077350850\"\n  }\n}\n",
	}, awsScope("eu-west-2"), nil)

	locals := strings.Join(strings.Fields(wsFile(t, ws, rootfile.LocalsFileName)), " ")
	for _, want := range []string{`role_arn = "arn:aws:iam::${local.aws_account_id}:role/app"`, `Account = "680077350850"`, "aws_account_id = data.aws_caller_identity.current.account_id"} {
		if !strings.Contains(locals, want) {
			t.Errorf("locals.tf doesn't have %s:\n%s", want, locals)
		}
	}
	if strings.Contains(locals, "aws_region") {
		t.Errorf("locals.tf reads the region, which nothing holds:\n%s", locals)
	}

	if err := setLocals(ws, map[string]cty.Value{"tags": cty.ObjectVal(map[string]cty.Value{"Team": cty.StringVal("core")})}); err != nil {
		t.Fatal(err)
	}
	locals = strings.Join(strings.Fields(wsFile(t, ws, rootfile.LocalsFileName)), " ")
	for _, want := range []string{"aws_account_id = data.aws_caller_identity.current.account_id", `role_arn = "arn:aws:iam::${local.aws_account_id}:role/app"`, `Team = "core"`} {
		if !strings.Contains(locals, want) {
			t.Errorf("setLocals lost %s:\n%s", want, locals)
		}
	}
}

// A local of the same name that holds something else, in the root or in
// the names taken, keeps the account literal; the root an incremental
// import adds to, which reads it the same way, has it read from its local.
func TestLiftScopeLeavesANameTaken(t *testing.T) {
	t.Parallel()
	src := "resource \"aws_sns_topic_policy\" \"a\" {\n  arn = \"arn:aws:sns:eu-west-2:680077350850:alerts\"\n}\n"
	scope := &ScopeLocals{Account: awsScope("").Account}

	ws := lifted(t, map[string]string{rootfile.GeneratedFileName: src, rootfile.LocalsFileName: "locals {\n  aws_account_id = \"111111111111\"\n}\n"}, scope, nil)
	if got := generatedText(t, ws); got != src {
		t.Errorf("lifted with local.aws_account_id taken:\n%s", got)
	}
	ws = lifted(t, map[string]string{rootfile.GeneratedFileName: src}, scope, Names{"local.aws_account_id": true})
	if got := generatedText(t, ws); got != src {
		t.Errorf("lifted with the name taken by the root:\n%s", got)
	}
	ws = lifted(t, map[string]string{rootfile.GeneratedFileName: src}, scope, Names{"local.aws_account_id": true, "data.aws_caller_identity.current": true})
	if got := generatedText(t, ws); !strings.Contains(got, "${local.aws_account_id}") {
		t.Errorf("not lifted where the root reads the account the same way:\n%s", got)
	}
}

// IDs are the parts of ARNs, paths, hosts and emails, or whole values: a
// longer number, a name that only starts with the region, and a name
// made of the project's ID hold neither.
func TestScopeWordIndex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s, id string
		want  int
	}{
		{"arn:aws:iam::123456789012:role/ci", "123456789012", 13},
		{"arn:aws:lambda:eu-west-2:123456789012:function:app", "eu-west-2", 15},
		{"123456789012", "123456789012", 0},
		{"1234567890123", "123456789012", -1},
		{"eu-west-2a", "eu-west-2", -1},
		{"s3.eu-west-2.amazonaws.com", "eu-west-2", 3},
		{"https://sqs.eu-west-2.amazonaws.com/123456789012/orders", "123456789012", 36},
		{"logs-eu-west-2", "eu-west-2", -1},
		{"eu-west-2-logs", "eu-west-2", -1},
		{"infraharvest-e2e-added", "infraharvest-e2e", -1},
		{"projects/infraharvest-e2e/topics/orders", "infraharvest-e2e", 9},
		{"app@infraharvest-e2e.iam.gserviceaccount.com", "infraharvest-e2e", 4},
		{"/subscriptions/1d07be38-3c3a-4758-bc19-1df3fc4daea6/resourceGroups/app", "1d07be38-3c3a-4758-bc19-1df3fc4daea6", 15},
		{"$123456789012", "123456789012", -1},
		{"", "123456789012", -1},
	} {
		if got := scopeWordIndex(tc.s, tc.id); got != tc.want {
			t.Errorf("scopeWordIndex(%q, %q) = %d, want %d", tc.s, tc.id, got, tc.want)
		}
	}
}

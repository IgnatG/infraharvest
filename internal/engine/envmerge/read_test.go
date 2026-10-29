// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// A root that reads its own account and region through locals of data
// sources, as engine.Generate writes it, reads as the literals they stand
// for, as the root would have them without: merging compares values, and
// the merged root reads its environments' account and region itself. The
// data sources nothing else reads go; another stays.
func TestReadEnvRootPutsInTheScopeLocals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"lambda.tf": `resource "aws_lambda_permission" "proxy" {
  function_name = "arn:aws:lambda:${local.aws_region}:${local.aws_account_id}:function:proxy-fn"
  source_vpc    = data.aws_vpc.shared.id
}

resource "aws_cloudwatch_metric_alarm" "errors" {
  alarm_actions = ["arn:aws:sns:${local.aws_region}:${local.aws_account_id}:events"]
  account       = local.aws_account_id
}
`,
		rootfile.LocalsFileName: "locals {\n  aws_account_id = data.aws_caller_identity.current.account_id\n  aws_region     = data.aws_region.current.region\n}\n",
		rootfile.DataFileName:   "data \"aws_caller_identity\" \"current\" {}\n\ndata \"aws_region\" \"current\" {}\n\ndata \"aws_vpc\" \"shared\" {\n  id = \"vpc-1\"\n}\n",
		rootfile.ImportsFileName: `import {
  to = aws_lambda_permission.proxy
  id = "proxy-fn/sns"
}

import {
  to = aws_cloudwatch_metric_alarm.errors
  id = "errors"
}
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data := map[string]map[string]string{
		"aws_caller_identity": {"account_id": "680077350850", "id": "680077350850"},
		"aws_region":          {"region": "eu-west-2", "name": "eu-west-2", "id": "eu-west-2"},
	}
	root, reason, err := readEnvRoot(MergeRoot{Env: "dev", Dir: dir, Scope: "680077350850", Region: "eu-west-2", Data: data, Provider: rootfile.Provider{Name: "aws"}}, t.TempDir())
	if err != nil || reason != "" {
		t.Fatalf("readEnvRoot: %v %s", err, reason)
	}
	got := string(root.file.Edit().Bytes())
	for _, want := range []string{
		`function_name = "arn:aws:lambda:eu-west-2:680077350850:function:proxy-fn"`,
		`alarm_actions = ["arn:aws:sns:eu-west-2:680077350850:events"]`,
		`account       = "680077350850"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the root doesn't read %s:\n%s", want, got)
		}
	}
	if blocks := dataBlocksOf(root.data); len(blocks) != 1 || blocks[0].Address != "aws_vpc.shared" {
		t.Errorf("data sources: %+v", blocks)
	}

	// Without the environment's values, the locals stay: nothing is put
	// in that isn't known.
	root, _, err = readEnvRoot(MergeRoot{Env: "dev", Dir: dir, Provider: rootfile.Provider{Name: "aws"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(root.file.Edit().Bytes()); !strings.Contains(got, "${local.aws_account_id}") {
		t.Errorf("put in without the environment's values:\n%s", got)
	}
	if blocks := dataBlocksOf(root.data); len(blocks) != 3 {
		t.Errorf("data sources: %+v", blocks)
	}
}

// A string local interpolated in a template joins the template's text, so
// that the value reads as the literal it is; one that isn't a string, or
// whose text would need escaping, is put in as a value.
func TestReplaceLocalsJoinsTemplateText(t *testing.T) {
	t.Parallel()
	values := map[string]cty.Value{
		"account": cty.StringVal("111111111111"),
		"quoted":  cty.StringVal(`a"b`),
		"port":    cty.NumberIntVal(80),
	}
	for in, want := range map[string]string{
		`"arn:aws:iam::${local.account}:role/ci"`: `"arn:aws:iam::111111111111:role/ci"`,
		`"${local.account}"`:                      `"111111111111"`,
		`"${local.account}-${local.account}"`:     `"111111111111-111111111111"`,
		`local.account`:                           `"111111111111"`,
		`"x-${local.quoted}"`:                     `"x-${"a\"b"}"`,
		`"p${local.port}"`:                        `"p${80}"`,
		`"${local.other}:1"`:                      `"${local.other}:1"`,
	} {
		got := strings.TrimSpace(string(replaceLocals(hclfile.ParseExpr(in), values).Bytes()))
		if got != want {
			t.Errorf("replaceLocals(%s) = %s, want %s", in, got, want)
		}
	}
}

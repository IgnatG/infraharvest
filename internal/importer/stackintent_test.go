// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
	"github.com/IgnatG/infraharvest/internal/stackintent"
)

// The root of a --cfn-stack import gets the stack's parameters and outputs
// for the resources it imports, matched by the call's scope, type and
// import ID.
func TestStackIntent(t *testing.T) {
	stack := cfn.Stack{
		Name: "app", ID: "arn:aws:cloudformation:eu-west-2:123456789012:stack/app/1",
		Resources: []cfn.Resource{
			{LogicalID: "Logs", Type: "AWS::Logs::LogGroup", Matches: []cfn.Match{{Scope: "123456789012/eu-west-2", Type: "aws_cloudwatch_log_group", ID: "/app/logs"}}},
			// Listed by another call: not this root's.
			{LogicalID: "Role", Type: "AWS::IAM::Role", Matches: []cfn.Match{{Scope: "123456789012/global", Type: "aws_iam_role", ID: "app"}}},
		},
	}
	stack.SetTemplates(`
Parameters:
  Days: {Type: Number}
Resources:
  Logs:
    Type: AWS::Logs::LogGroup
    Properties:
      LogGroupName: /app/logs
      RetentionInDays: !Ref Days
  Role:
    Type: AWS::IAM::Role
Outputs:
  LogGroupArn:
    Value: !GetAtt Logs.Arn
  RoleArn:
    Value: !GetAtt Role.Arn
`, "", "yaml")
	stack.SetParameters(map[string]string{"Days": "7"}, nil)
	run := NewRun()
	run.report.CloudFormationStacks.Add([]cfn.Stack{stack})
	c := &listedCall{callRecorder: callRecorder{run: run, scope: "123456789012/eu-west-2"}, provider: &fakeProvider{}}
	refine, key := c.stackIntent()
	if refine == nil {
		t.Fatal("no Refine for a scoped call")
	}
	if key == "" {
		t.Error("no RefineKey for a scoped call")
	}

	dir := t.TempDir()
	logs := "resource \"aws_cloudwatch_log_group\" \"logs\" {\n  name              = \"/app/logs\"\n  retention_in_days = 7\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "logs.tf"), []byte(logs), 0o644); err != nil {
		t.Fatal(err)
	}
	warnings, err := refine(t.Context(), dir, []engine.Managed{{Type: "aws_cloudwatch_log_group", ID: "/app/logs", Address: "aws_cloudwatch_log_group.logs"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "logs.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "retention_in_days = var.days") {
		t.Errorf("logs.tf:\n%s", got)
	}
	added, err := os.ReadFile(filepath.Join(dir, stackintent.FileName("app")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`variable "days"`, `default     = 7`, `output "log_group_arn"`, `aws_cloudwatch_log_group.logs.arn`} {
		if !strings.Contains(string(added), want) {
			t.Errorf("%s lacks %s:\n%s", stackintent.FileName("app"), want, added)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "RoleArn") {
		t.Errorf("warnings = %q", warnings)
	}

	// A call that lists none of the stack's resources has nothing to refine.
	other := &listedCall{callRecorder: callRecorder{run: run, scope: "210987654321/eu-west-2"}, provider: &fakeProvider{}}
	if refine, _ := other.stackIntent(); refine != nil {
		t.Error("a Refine for a call with none of the stack's resources")
	}
}

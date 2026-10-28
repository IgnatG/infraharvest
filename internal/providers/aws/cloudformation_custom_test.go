// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/xml"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
)

const (
	cdkStack   = "arn:aws:cloudformation:eu-west-2:123456789012:stack/cdk/1"
	otherStack = "arn:aws:cloudformation:eu-west-2:123456789012:stack/other/2"
)

// cdkTemplate is a CDK app's: a bucket with autoDeleteObjects, a Lambda
// function's log retention, a cluster made by eks.Cluster, and a custom
// resource of the app's own.
const cdkTemplate = `Resources:
  Assets:
    Type: AWS::S3::Bucket
  AssetsAutoDelete:
    Type: Custom::S3AutoDeleteObjects
    Properties:
      ServiceToken: !GetAtt Provider.Arn
      BucketName: !Ref Assets
  Retention:
    Type: Custom::LogRetention
    Properties:
      LogGroupName: /aws/lambda/cdk-fn
      RetentionInDays: 7
  Cluster:
    Type: Custom::AWSCDK-EKS-Cluster
  Seed:
    Type: Custom::Seed
`

// cdkStacksAPI answers with the stack cdk, and other, which has a
// resource Seed too.
func cdkStacksAPI(t *testing.T) func(apiCall) string {
	member := func(logical, physical, typ string) string {
		return `<member><LogicalResourceId>` + logical + `</LogicalResourceId><PhysicalResourceId>` + physical + `</PhysicalResourceId><ResourceType>` + typ +
			`</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>`
	}
	return func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		stack := form.Get("StackName")
		id := map[string]string{"cdk": cdkStack, "other": otherStack}[stack]
		switch call.Op {
		case "DescribeStacks":
			return recordQuery(call.Op, recordCfnNS, `<Stacks><member><StackId>`+id+`</StackId><StackName>`+stack+`</StackName><StackStatus>CREATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`)
		case "GetTemplate":
			body := "Resources:\n  Seed:\n    Type: Custom::Seed\n"
			if stack == cdkStack {
				body = cdkTemplate
			}
			var b strings.Builder
			_ = xml.EscapeText(&b, []byte(body))
			return recordQuery(call.Op, recordCfnNS, `<TemplateBody>`+b.String()+`</TemplateBody>`)
		case "ListStackResources":
			if stack == otherStack {
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+member("Seed", "seed-2", "Custom::Seed")+`</StackResourceSummaries>`)
			}
			// The custom resource comes before the bucket it names.
			return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+
				member("AssetsAutoDelete", "cdk-AssetsAutoDelete-1ABC", "Custom::S3AutoDeleteObjects")+
				member("Assets", "cdk-assets-1xyz", "AWS::S3::Bucket")+
				member("Retention", "/aws/lambda/cdk-fn", "Custom::LogRetention")+
				member("Cluster", "cdk-cluster", "Custom::AWSCDK-EKS-Cluster")+
				member("Seed", "seed-1", "Custom::Seed")+
				`</StackResourceSummaries>`)
		}
		return recordUnexpected(t, call)
	}
}

// The CDK's custom resources get advice of their own, naming what they
// act on from the template, a Ref resolved to the physical ID though it
// comes later; Custom::S3AutoDeleteObjects must be taken out of the stack
// first; eks.Cluster's cluster is imported as aws_eks_cluster; and another
// custom resource gets the general advice.
func TestCDKCustomResources(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, cdkStacksAPI(t))
	scope, err := ResolveStacks(ctx, "default", "", "eu-west-2", []string{"cdk"}, awsCatalogue())
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]*scopedResource{}
	for _, r := range scope.stacks[0].resources {
		got[r.logicalID] = r
	}
	if r := got["AssetsAutoDelete"]; !r.mustRemove || !strings.Contains(r.notImportable, "every object of bucket cdk-assets-1xyz") || !strings.Contains(r.notImportable, "aws-cdk:auto-delete-objects") {
		t.Errorf("AssetsAutoDelete: %+v", r)
	}
	if r := got["Retention"]; r.mustRemove || !strings.Contains(r.notImportable, "log group /aws/lambda/cdk-fn") {
		t.Errorf("Retention: %+v", r)
	}
	if r := got["Cluster"]; r.notImportable != "" {
		t.Errorf("Cluster isn't imported: %+v", r)
	}
	if r := got["Seed"]; r.notImportable != genericCustomResource {
		t.Errorf("Seed: %+v", r)
	}
	if services := scope.Services(awsCatalogue()); !strings.Contains(strings.Join(services, ","), "eks") {
		t.Errorf("services %v lack eks", services)
	}
}

// --cfn-ignore acknowledges what Terraform can't import, by logical ID or,
// where two stacks have it, by stack too; it refuses what must be taken
// out of the stack first, what can be imported, and what isn't there.
func TestAcknowledge(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, cdkStacksAPI(t))
	scope, err := ResolveStacks(ctx, "default", "", "eu-west-2", []string{"cdk", "other"}, awsCatalogue())
	if err != nil {
		t.Fatal(err)
	}

	for entry, want := range map[string]string{
		"Seed":             "stacks cdk, other each have it: name one, as <stack>/Seed",
		"AssetsAutoDelete": "can't be acknowledged",
		"Assets":           "only a resource Terraform can't import",
		"cdk/Missing":      "no resource of the stacks has that logical ID",
	} {
		if err := scope.Acknowledge([]string{entry}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", entry, err, want)
		}
	}
	if err := scope.Acknowledge([]string{"cdk/Seed", "Retention"}); err != nil {
		t.Fatal(err)
	}
	acknowledged := map[string]bool{}
	for _, s := range scope.stacks {
		for _, r := range s.resources {
			if r.acknowledged {
				acknowledged[s.name+"/"+r.logicalID] = true
			}
		}
	}
	if len(acknowledged) != 2 || !acknowledged["cdk/Seed"] || !acknowledged["cdk/Retention"] {
		t.Errorf("acknowledged %v", acknowledged)
	}
	summary := strings.Join(scope.Summary(), "\n")
	if !strings.Contains(summary, "Seed (Custom::Seed), acknowledged") || !strings.Contains(summary, "must be taken out of the stack") {
		t.Errorf("summary:\n%s", summary)
	}
	// What is acknowledged still needs Retain, so that deleting the stack
	// never calls its handler: the count of what lacks it, and the copy with
	// Retain added, have it too.
	if !strings.Contains(summary, "lack DeletionPolicy and UpdateReplacePolicy Retain, so deleting the stack would delete them: AssetsAutoDelete, Assets, Retention, Cluster, Seed\n") {
		t.Errorf("summary leaves out what is acknowledged as lacking Retain:\n%s", summary)
	}
	r := &report.Report{}
	r.CloudFormationStacks.Add([]cfn.Stack{scope.stacks[0].report(nil)})
	r.Finish(nil, nil, false)
	out := t.TempDir()
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	patched, err := parseTemplate(readTestFile(t, filepath.Join(out, filepath.FromSlash(r.CloudFormationStacks[0].RetainTemplate))))
	if err != nil {
		t.Fatal(err)
	}
	for id, wantRetained := range map[string]bool{"Assets": true, "AssetsAutoDelete": true, "Seed": true, "Retention": true} {
		if retained := patched.retainProblems(id, typeOf(scope, id)) == nil; retained != wantRetained {
			t.Errorf("%s retained in the copy: %v, want %v", id, retained, wantRetained)
		}
	}
}

// typeOf returns the CloudFormation type of the resource logicalID of the
// first stack of scope.
func typeOf(scope *StackScope, logicalID string) string {
	for _, r := range scope.stacks[0].resources {
		if r.logicalID == logicalID {
			return r.cfnType
		}
	}
	return ""
}

// readTestFile returns the content of the file at path.
func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// A queue's policy, which Terraform holds as the queue's policy argument,
// is imported where the queue it names is, in the template's JSON form of
// Ref too.
func TestImportScopeHeldPolicy(t *testing.T) {
	t.Parallel()
	const queueURL = "https://sqs.eu-west-2.amazonaws.com/123456789012/jobs"
	template, err := parseTemplate(`{"Resources": {"Jobs": {"Type": "AWS::SQS::Queue"}, "JobsPolicy": {"Type": "AWS::SQS::QueuePolicy", "Properties": {"Queues": [{"Ref": "Jobs"}]}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	stack := &scopedStack{name: "app", id: cdkStack, region: "eu-west-2", template: template, resources: []*scopedResource{
		{logicalID: "Jobs", cfnType: "AWS::SQS::Queue", physicalID: queueURL, status: "CREATE_COMPLETE"},
		{logicalID: "JobsPolicy", cfnType: "AWS::SQS::QueuePolicy", physicalID: "app-JobsPolicy-1ABC", status: "CREATE_COMPLETE"},
	}}
	p := &Provider{region: "eu-west-2"}
	p.SetStackScope(&StackScope{stacks: []*scopedStack{stack}})

	_, err = p.ImportScope(t.Context(), []harvest.Resource{harvest.RecordBare(queueURL, "jobs", "aws_sqs_queue", "aws")})
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range p.CloudFormationStacks()[0].Resources {
		if len(r.Matches) != 1 || r.Matches[0].Type != "aws_sqs_queue" || r.Matches[0].ID != queueURL {
			t.Errorf("%s: %+v", r.LogicalID, r.Matches)
		}
	}
}

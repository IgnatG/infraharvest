// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const stackScope = "aws/123456789012/eu-west-2"

// scopedStack is a stack whose resources were all imported, but its
// bucket, whose DeletionPolicy isn't Retain.
func scopedStack(retain []string) CloudFormationStack {
	s := CloudFormationStack{
		Name: "app", ID: "arn:aws:cloudformation:eu-west-2:123456789012:stack/app/1", Region: "eu-west-2", Status: "UPDATE_COMPLETE",
		Resources: []CloudFormationResource{
			{LogicalID: "Assets", Type: "AWS::S3::Bucket", PhysicalID: "app-assets", Status: "CREATE_COMPLETE", Retain: retain,
				Matches: []CloudFormationMatch{{Scope: stackScope, Type: "aws_s3_bucket", ID: "app-assets"}}},
			{LogicalID: "CDKMetadata", Type: "AWS::CDK::Metadata", PhysicalID: "meta", Status: "CREATE_COMPLETE", Outcome: StackOnlyInCloudFormation, NotImportable: "the CDK's metadata"},
		},
	}
	s.SetTemplates("Resources: {}\n", "Resources: {Assets: {DeletionPolicy: Retain}}\n", "yaml")
	return s
}

// imports returns a directory of the scope that imports the bucket.
func imports(checks ...Check) Directory {
	return Directory{Path: "aws/123456789012/eu-west-2", Scope: stackScope, Imported: []Resource{{Address: "module.s3.aws_s3_bucket.this", ID: "app-assets"}}, Checks: checks}
}

// A stack whose resources were all imported, by a root whose gate passed,
// and are all retained is safe to delete; each resource says where it
// went.
func TestStackSafeToDelete(t *testing.T) {
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{scopedStack(nil)})
	r.Directories = []Directory{imports(Check{Name: "G3 plan", Passed: true})}

	r.Finish(nil, nil, false)

	s := r.CloudFormationStacks[0]
	if !s.SafeToDelete || s.Blockers != nil {
		t.Fatalf("not safe: %v", s.Blockers)
	}
	want := CloudFormationMatch{Scope: stackScope, Type: "aws_s3_bucket", ID: "app-assets", Path: "aws/123456789012/eu-west-2", Address: "module.s3.aws_s3_bucket.this"}
	if s.Resources[0].Outcome != StackImported || !reflect.DeepEqual(s.Resources[0].Matches, []CloudFormationMatch{want}) {
		t.Errorf("Assets: %+v", s.Resources[0])
	}
	if s.Resources[1].Outcome != StackOnlyInCloudFormation {
		t.Errorf("CDKMetadata: %+v", s.Resources[1])
	}
	if !strings.Contains(r.Markdown(), "**Safe to delete**") {
		t.Errorf("markdown:\n%s", r.Markdown())
	}
}

// What keeps a stack from being safe to delete is each said: a resource
// not retained, one excluded by the default selection, one not found, one
// not importable, and a root whose gate failed. The selection file's
// exclusions don't count; nor does anything of a deleted stack.
func TestStackBlockers(t *testing.T) {
	s := scopedStack([]string{"DeletionPolicy is Delete, the default"})
	s.Resources = append(s.Resources,
		CloudFormationResource{LogicalID: "Queue", Type: "AWS::SQS::Queue", PhysicalID: "q", Status: "CREATE_COMPLETE", Matches: []CloudFormationMatch{{Scope: stackScope, Type: "aws_sqs_queue", ID: "q"}}},
		CloudFormationResource{LogicalID: "Topic", Type: "AWS::SNS::Topic", PhysicalID: "t", Status: "CREATE_COMPLETE", Matches: []CloudFormationMatch{{Scope: stackScope, Type: "aws_sns_topic", ID: "t"}}},
		CloudFormationResource{LogicalID: "Method", Type: "AWS::ApiGateway::Method", PhysicalID: "m-1", Status: "CREATE_COMPLETE"},
		CloudFormationResource{LogicalID: "Seed", Type: "Custom::Seed", PhysicalID: "seed", Status: "CREATE_COMPLETE", NotImportable: "a custom resource"},
	)
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{s})
	r.Directories = []Directory{imports(Check{Name: "G3 plan", Passed: false, Details: []string{"aws_s3_bucket.this updates tags"}})}
	r.Excluded = []Excluded{
		{Type: "aws_sqs_queue", ID: "q", Reason: ReasonSelectionEntry, Scope: stackScope},
		{Type: "aws_sns_topic", ID: "t", Reason: "default VPC", Scope: stackScope},
	}

	r.Finish(nil, nil, false)

	got := r.CloudFormationStacks[0]
	if got.SafeToDelete {
		t.Fatal("safe to delete")
	}
	blockers := strings.Join(got.Blockers, "\n")
	for _, want := range []string{
		"Assets (AWS::S3::Bucket) isn't retained: DeletionPolicy is Delete, the default",
		"Topic (AWS::SNS::Topic) isn't imported, as aws_sns_topic t: excluded: default VPC",
		"Method (AWS::ApiGateway::Method) wasn't found: nothing listed has its physical ID m-1",
		"Seed (Custom::Seed) can't be imported: a custom resource",
		"the gate of `aws/123456789012/eu-west-2`",
	} {
		if !strings.Contains(blockers, want) {
			t.Errorf("blockers lack %q:\n%s", want, blockers)
		}
	}
	if strings.Contains(blockers, "Queue") {
		t.Errorf("the selection file's exclusion blocks:\n%s", blockers)
	}
	md := r.Markdown()
	for _, want := range []string{"**Not safe to delete yet**", "app.retain.template.yaml", "aws cloudformation update-stack --stack-name app"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}

	deleted := scopedStack([]string{"DeletionPolicy is Delete, the default"})
	deleted.Deleted = true
	r = &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{deleted})
	r.Finish(nil, nil, false)
	if got := r.CloudFormationStacks[0]; got.SafeToDelete || got.Blockers != nil {
		t.Errorf("deleted stack: %+v", got)
	}
}

// Calls of one import that find parts of the same stack add up, the
// global call's role to the regional call's bucket; and the report writes
// the templates where it says, only the user can read them.
func TestStacksMergeAndWrite(t *testing.T) {
	regional := scopedStack(nil)
	global := scopedStack(nil)
	global.Resources[0].Matches = []CloudFormationMatch{{Scope: "aws/123456789012/aws-global", Type: "aws_s3_bucket_policy", ID: "app-assets"}}
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{regional})
	other := &Report{}
	other.AddCloudFormationStacks([]CloudFormationStack{global})
	r.Merge(other)

	if n := len(r.CloudFormationStacks); n != 1 {
		t.Fatalf("%d stacks", n)
	}
	if n := len(r.CloudFormationStacks[0].Resources[0].Matches); n != 2 {
		t.Errorf("%d matches, want both calls'", n)
	}

	out := t.TempDir()
	r.Finish(nil, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	s := r.CloudFormationStacks[0]
	if s.Template != ".infraharvest/cloudformation/eu-west-2/app.template.yaml" || s.RetainTemplate != ".infraharvest/cloudformation/eu-west-2/app.retain.template.yaml" {
		t.Errorf("paths %q, %q", s.Template, s.RetainTemplate)
	}
	content, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(s.RetainTemplate)))
	if err != nil || !strings.Contains(string(content), "Retain") {
		t.Errorf("retain template %q, %v", content, err)
	}
	coverage, err := os.ReadFile(filepath.Join(out, Dir, "coverage.json"))
	if err != nil || !strings.Contains(string(coverage), `"logical_id": "Assets"`) {
		t.Errorf("coverage.json lacks the stack: %v", err)
	}
}

// An acknowledged custom resource doesn't keep its stack from being safe
// to delete, retained or not, though the report says that deleting the
// stack calls its handler where it isn't; but one that must be taken out of the stack first does,
// acknowledged or not.
func TestStackCustomResources(t *testing.T) {
	s := scopedStack(nil)
	s.Resources = append(s.Resources,
		CloudFormationResource{LogicalID: "Seed", Type: "Custom::Seed", PhysicalID: "seed", Status: "CREATE_COMPLETE", NotImportable: "a custom resource", Acknowledged: true, Retain: []string{"DeletionPolicy is Delete, the default"}},
	)
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{s})
	r.Directories = []Directory{imports()}
	r.Finish(nil, nil, false)
	got := r.CloudFormationStacks[0]
	if !got.SafeToDelete || got.Resources[2].Outcome != StackAcknowledged || !strings.Contains(got.Resources[2].Reason, "deleting the stack calls its handler with a Delete event") {
		t.Errorf("acknowledged, not retained: %+v", got)
	}

	s.Resources = append(s.Resources, CloudFormationResource{LogicalID: "AutoDelete", Type: "Custom::S3AutoDeleteObjects", PhysicalID: "ad", Status: "CREATE_COMPLETE", NotImportable: "the CDK's autoDeleteObjects", MustRemove: true})
	r = &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{s})
	r.Directories = []Directory{imports()}
	r.Finish(nil, nil, false)
	got = r.CloudFormationStacks[0]
	if got.SafeToDelete || len(got.Blockers) != 1 || !strings.Contains(got.Blockers[0], "AutoDelete (Custom::S3AutoDeleteObjects) must be taken out of the stack before the stack is deleted") {
		t.Errorf("must remove: %v", got.Blockers)
	}
}

// Deleting a stack deletes the stacks nested in it, so what keeps one of
// those from being safe to delete keeps the stack it is nested in too.
func TestStackNestedBlockers(t *testing.T) {
	parent := scopedStack(nil)
	nested := CloudFormationStack{Name: "app-Network-1ABC", ID: "stack/app-Network-1ABC", Region: "eu-west-2", Parent: parent.ID, Resources: []CloudFormationResource{
		{LogicalID: "Alerts", Type: "AWS::SNS::Topic", PhysicalID: "alerts", Status: "CREATE_COMPLETE", Retain: []string{"DeletionPolicy is Delete, the default"}, Matches: []CloudFormationMatch{{Scope: stackScope, Type: "aws_sns_topic", ID: "alerts"}}},
	}}
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{parent, nested})
	r.Directories = []Directory{imports()}
	r.Directories[0].Imported = append(r.Directories[0].Imported, Resource{Address: "aws_sns_topic.alerts", ID: "alerts"})

	r.Finish(nil, nil, false)

	for _, s := range r.CloudFormationStacks {
		if s.SafeToDelete {
			t.Errorf("%s is safe to delete", s.Name)
		}
		if s.Name == "app" && !strings.Contains(strings.Join(s.Blockers, "\n"), "in nested stack app-Network-1ABC: Alerts (AWS::SNS::Topic) isn't retained") {
			t.Errorf("app's blockers: %v", s.Blockers)
		}
	}
}

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

// A deleted stack and the live one of the same name, in one region, write
// their templates to files of their own.
func TestStacksOfOneName(t *testing.T) {
	deleted := scopedStack(nil)
	deleted.ID, deleted.Deleted, deleted.Status = "arn:aws:cloudformation:eu-west-2:123456789012:stack/app/0", true, "DELETE_COMPLETE"
	deleted.SetTemplates("Resources: {Old: {}}\n", "", "yaml")
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{scopedStack(nil), deleted})
	r.Finish(nil, nil, false)
	out := t.TempDir()
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, s := range r.CloudFormationStacks {
		paths[s.ID] = s.Template
	}
	want := map[string]string{
		"arn:aws:cloudformation:eu-west-2:123456789012:stack/app/1": ".infraharvest/cloudformation/eu-west-2/app-1.template.yaml",
		"arn:aws:cloudformation:eu-west-2:123456789012:stack/app/0": ".infraharvest/cloudformation/eu-west-2/app-0.template.yaml",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("templates %v, want %v", paths, want)
	}
	content, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(want[deleted.ID])))
	if err != nil || string(content) != "Resources: {Old: {}}\n" {
		t.Errorf("deleted stack's template %q, %v", content, err)
	}
}

// An acknowledged custom resource doesn't keep its stack from being safe
// to delete, but one that must be taken out of the stack first does,
// acknowledged or not.
func TestStackCustomResources(t *testing.T) {
	s := scopedStack(nil)
	s.Resources = append(s.Resources,
		CloudFormationResource{LogicalID: "Seed", Type: "Custom::Seed", PhysicalID: "seed", Status: "CREATE_COMPLETE", NotImportable: "a custom resource", Acknowledged: true},
	)
	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{s})
	r.Directories = []Directory{imports()}
	r.Finish(nil, nil, false)
	got := r.CloudFormationStacks[0]
	if !got.SafeToDelete || got.Resources[2].Outcome != StackAcknowledged {
		t.Errorf("acknowledged: %+v", got)
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

// A copy of a template larger than UpdateStack takes in its request is
// to be uploaded to S3, and the stack updated from there; the report read
// back, which has no templates, says the same.
func TestStackUpdateCommand(t *testing.T) {
	for size, want := range map[int]string{100: "--template-body file://", templateBodyLimit + 1: "--template-url https://<bucket>.s3.eu-west-2.amazonaws.com/app.retain.template.yaml"} {
		s := scopedStack([]string{"DeletionPolicy is Delete, the default"})
		s.SetTemplates("Resources: {}\n", strings.Repeat("#", size), "yaml")
		r := &Report{}
		r.AddCloudFormationStacks([]CloudFormationStack{s})
		r.Finish(nil, nil, false)
		if md := r.Markdown(); !strings.Contains(md, want) {
			t.Errorf("%d bytes: the report lacks %q", size, want)
		}
		out := t.TempDir()
		if err := r.WriteFiles(out); err != nil {
			t.Fatal(err)
		}
		read, err := Read(out)
		if err != nil {
			t.Fatal(err)
		}
		if md := read.Markdown(); !strings.Contains(md, want) {
			t.Errorf("%d bytes: the report read back lacks %q", size, want)
		}
	}
}

// A nested stack is updated through its root stack: its copy is uploaded
// where its parent's TemplateURL points, and the root updated, with its
// own copy if it has one, else with its template as it is; the report
// read back says the same. One a deleted stack retained is updated
// itself.
func TestStackNestedUpdate(t *testing.T) {
	root := scopedStack(nil)
	root.SetTemplates("Resources: {}\n", "", "yaml")
	nested := CloudFormationStack{Name: "app-Network-1ABC", ID: "arn:aws:cloudformation:eu-west-2:123456789012:stack/app-Network-1ABC/2", Region: "eu-west-2", Parent: root.ID,
		Root: root.ID, NestedAs: "Network", TemplateURL: "https://cdk-assets.s3.eu-west-2.amazonaws.com/network.json"}
	nested.SetTemplates("Resources: {}\n", "Resources: {Alerts: {DeletionPolicy: Retain}}\n", "yaml")
	copied := "aws s3 cp .infraharvest/cloudformation/eu-west-2/app-Network-1ABC.retain.template.yaml s3://cdk-assets/network.json"
	previous := "`aws cloudformation update-stack --stack-name app --use-previous-template"

	r := &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{root, nested})
	r.Finish(nil, nil, false)
	md := r.Markdown()
	for _, want := range []string{copied, "its parent's `Network`", previous} {
		if !strings.Contains(md, want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "--stack-name app-Network-1ABC") {
		t.Errorf("the nested stack is updated itself:\n%s", md)
	}
	out := t.TempDir()
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	read, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := read.stack(nested.ID); got == nil || got.Root != root.ID || got.NestedAs != "Network" || got.TemplateURL != nested.TemplateURL {
		t.Errorf("read back: %+v", got)
	}
	if md := read.Markdown(); !strings.Contains(md, copied) || !strings.Contains(md, previous) {
		t.Errorf("the report read back lacks the update through the root:\n%s", md)
	}

	r = &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{scopedStack([]string{"DeletionPolicy is Delete, the default"}), nested})
	r.Finish(nil, nil, false)
	md = r.Markdown()
	for _, want := range []string{"update the root stack `app` with its own copy", "--template-body file://.infraharvest/cloudformation/eu-west-2/app.retain.template.yaml", "once the copies of the stacks nested in it are uploaded"} {
		if !strings.Contains(md, want) {
			t.Errorf("root with a copy: the report lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "--use-previous-template") {
		t.Errorf("root with a copy: updated with its previous template:\n%s", md)
	}

	gone := scopedStack(nil)
	gone.Deleted = true
	r = &Report{}
	r.AddCloudFormationStacks([]CloudFormationStack{gone, nested})
	r.Finish(nil, nil, false)
	if md := r.Markdown(); !strings.Contains(md, "update-stack --stack-name app-Network-1ABC --template-body") || strings.Contains(md, "root stack `app`") {
		t.Errorf("retained by a deleted parent:\n%s", md)
	}
}

// An S3 URL becomes the S3 URI the upload takes only where its bucket and
// key are safe in a shell command as they are: the URL is a template's.
func TestS3Object(t *testing.T) {
	for in, want := range map[string]string{
		"https://bkt.s3.eu-west-2.amazonaws.com/k/t.json":   "s3://bkt/k/t.json",
		"https://bkt.s3.amazonaws.com/t.json":               "s3://bkt/t.json",
		"https://my.s3.bucket.s3-eu-west-1.amazonaws.com/t": "s3://my.s3.bucket/t",
		"https://s3.eu-west-2.amazonaws.com/bkt/k/t.json":   "s3://bkt/k/t.json",
		"https://s3.amazonaws.com/bkt/t.json":               "s3://bkt/t.json",
		"https://s3.amazonaws.com/bkt":                      "",
		"https://example.com/t.json":                        "",
		"https://bkt.s3.amazonaws.com.evil.com/t.json":      "",
		"https://bkt.s3.amazonaws.com/t.json?x=1":           "",
		"https://bkt.s3.amazonaws.com/t%20$(id).json":       "",
		"https://bkt.s3.amazonaws.com/t;rm -rf ~":           "",
		"https://bkt.s3.amazonaws.com/a`id`":                "",
		"https://bkt.s3.amazonaws.com/a%0Aid":               "",
		"https://bkt.s3.amazonaws.com/a!id.json":            "",
		"https://BKT_1.s3.amazonaws.com/t.json":             "",
		"":                                                  "",
	} {
		if got := s3Object(in); got != want {
			t.Errorf("s3Object(%q) = %q, want %q", in, got, want)
		}
	}
}

// A hostile TemplateURL or logical ID never reaches the commands the
// report gives: the upload takes a placeholder instead.
func TestStackNestedUpdateHostile(t *testing.T) {
	root := scopedStack(nil)
	root.SetTemplates("Resources: {}\n", "", "yaml")
	for _, hostile := range []string{
		"https://bkt.s3.amazonaws.com/`touch x`.json",
		"https://bkt.s3.amazonaws.com/$(touch x).json",
		"https://bkt.s3.amazonaws.com/t.json; touch x",
		"https://bkt.s3.amazonaws.com/touch!x.json",
		"https://bkt.s3.amazonaws.com/t.json\ntouch x",
		"`touch x`",
	} {
		nested := CloudFormationStack{Name: "app-Network-1ABC", ID: "arn:aws:cloudformation:eu-west-2:123456789012:stack/app-Network-1ABC/2", Region: "eu-west-2", Parent: root.ID,
			Root: root.ID, NestedAs: "Net`touch x`", TemplateURL: hostile}
		nested.SetTemplates("Resources: {}\n", "Resources: {Alerts: {DeletionPolicy: Retain}}\n", "yaml")
		r := &Report{}
		r.AddCloudFormationStacks([]CloudFormationStack{root, nested})
		r.Finish(nil, nil, false)
		md := r.Markdown()
		if strings.Contains(md, "touch x") {
			t.Errorf("%q reaches the report:\n%s", hostile, md)
		}
		if !strings.Contains(md, "aws s3 cp .infraharvest/cloudformation/eu-west-2/app-Network-1ABC.retain.template.yaml s3://<bucket>/<key>") {
			t.Errorf("%q: the upload lacks the placeholder:\n%s", hostile, md)
		}
	}
}

func TestShellArg(t *testing.T) {
	for in, want := range map[string]string{
		"app-1":                  "app-1",
		"file://.infraharvest/a": "file://.infraharvest/a",
		"it's":                   `'it'\''s'`,
		"a`b`\n$(c)":             "'ab$(c)'",
	} {
		if got := shellArg(in); got != want {
			t.Errorf("shellArg(%q) = %q, want %q", in, got, want)
		}
	}
}

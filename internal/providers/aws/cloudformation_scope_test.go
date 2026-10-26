// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/xml"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

const (
	scopeAppStack     = "arn:aws:cloudformation:eu-west-2:123456789012:stack/app/1"
	scopeNetworkStack = "arn:aws:cloudformation:eu-west-2:123456789012:stack/app-Network-1ABC/2"
	scopeOldStack     = "arn:aws:cloudformation:eu-west-2:123456789012:stack/old/3"
)

// scopeAppTemplate is app's template, in YAML with short-form tags: a
// bucket that isn't retained, its policy, which is, an RDS DB instance
// outside a cluster, whose DeletionPolicy is Snapshot by default, a custom
// resource, the CDK's metadata and a nested stack.
const scopeAppTemplate = `AWSTemplateFormatVersion: "2010-09-09"
Resources:
  Assets:
    Type: AWS::S3::Bucket
  AssetsPolicy:
    Type: AWS::S3::BucketPolicy
    DeletionPolicy: Retain
    UpdateReplacePolicy: Retain
    Properties:
      Bucket: !Ref Assets
  Db:
    Type: AWS::RDS::DBInstance
    UpdateReplacePolicy: Retain
    Properties:
      Engine: postgres
  Seed:
    Type: Custom::Seed
    Properties:
      ServiceToken: !GetAtt SeedFunction.Arn
  CDKMetadata:
    Type: AWS::CDK::Metadata
  Network:
    Type: AWS::CloudFormation::Stack
    Properties:
      TemplateURL: https://example.com/network.json
`

// scopeNetworkTemplate is the nested stack's, in JSON indented by tabs: a
// role, retained.
const scopeNetworkTemplate = "{\n\t\"Resources\": {\n\t\t\"Role\": {\"Type\": \"AWS::IAM::Role\", \"DeletionPolicy\": \"Retain\", \"UpdateReplacePolicy\": \"Retain\", \"Properties\": {\"MaxSessionDuration\": 3600}}\n\t}\n}"

// scopeStacksAPI answers CloudFormation with the stack app, its nested
// stack, and the stack old, deleted, which retained one bucket.
func scopeStacksAPI(t *testing.T) func(apiCall) string {
	member := func(logical, physical, typ, status string) string {
		return `<member><LogicalResourceId>` + logical + `</LogicalResourceId><PhysicalResourceId>` + physical + `</PhysicalResourceId><ResourceType>` + typ +
			`</ResourceType><ResourceStatus>` + status + `</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>`
	}
	escape := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		stack := form.Get("StackName")
		switch call.Op {
		case "DescribeStacks":
			switch stack {
			case "app", scopeAppStack:
				return recordQuery(call.Op, recordCfnNS, `<Stacks><member><StackId>`+scopeAppStack+`</StackId><StackName>app</StackName><StackStatus>UPDATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`)
			case scopeOldStack:
				return recordQuery(call.Op, recordCfnNS, `<Stacks><member><StackId>`+scopeOldStack+`</StackId><StackName>old</StackName><StackStatus>DELETE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`)
			case scopeNetworkStack:
				return recordQuery(call.Op, recordCfnNS, `<Stacks><member><StackId>`+scopeNetworkStack+`</StackId><StackName>app-Network-1ABC</StackName><StackStatus>UPDATE_COMPLETE</StackStatus>`+
					`<ParentId>`+scopeAppStack+`</ParentId><CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`)
			}
			return recordQueryError(recordCfnNS, "ValidationError")
		case "GetTemplate":
			if form.Get("TemplateStage") != "Processed" {
				t.Errorf("template asked for at stage %q, want Processed", form.Get("TemplateStage"))
			}
			body := map[string]string{scopeAppStack: scopeAppTemplate, scopeNetworkStack: scopeNetworkTemplate, scopeOldStack: "Resources: {}\n"}[stack]
			return recordQuery(call.Op, recordCfnNS, `<TemplateBody>`+escape(body)+`</TemplateBody>`)
		case "ListStackResources":
			switch stack {
			case scopeAppStack:
				if recordSecondPage(call) {
					return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+
						member("Seed", "seed-1", "Custom::Seed", "CREATE_COMPLETE")+
						member("CDKMetadata", "meta-1", "AWS::CDK::Metadata", "CREATE_COMPLETE")+
						member("Network", scopeNetworkStack, "AWS::CloudFormation::Stack", "CREATE_COMPLETE")+
						`</StackResourceSummaries>`)
				}
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+
					member("Assets", "app-assets", "AWS::S3::Bucket", "CREATE_COMPLETE")+
					member("AssetsPolicy", "app-AssetsPolicy-1XYZ", "AWS::S3::BucketPolicy", "CREATE_COMPLETE")+
					member("Db", "app-db", "AWS::RDS::DBInstance", "CREATE_COMPLETE")+
					`<member><LogicalResourceId>Failed</LogicalResourceId><ResourceType>AWS::SQS::Queue</ResourceType><ResourceStatus>CREATE_FAILED</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>`+
					`</StackResourceSummaries><NextToken>`+nextPageToken+`</NextToken>`)
			case scopeNetworkStack:
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+member("Role", "app-role", "AWS::IAM::Role", "CREATE_COMPLETE")+`</StackResourceSummaries>`)
			case scopeOldStack:
				return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries>`+
					member("Kept", "old-kept", "AWS::S3::Bucket", "DELETE_SKIPPED")+
					member("Gone", "old-gone", "AWS::S3::Bucket", "DELETE_COMPLETE")+
					`</StackResourceSummaries>`)
			}
		}
		return recordUnexpected(t, call)
	}
}

// resolveScope resolves app by name and old by its stack ID.
func resolveScope(t *testing.T) *StackScope {
	t.Helper()
	useFakeAPI(t, scopeStacksAPI(t))
	scope, err := ResolveStacks(t.Context(), "default", "", "eu-west-2", []string{"app", scopeOldStack, scopeAppStack}, awsCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

// A stack is resolved by name, a deleted one by its stack ID, and a stack
// named twice once; each stack's resources are read across pages, with
// its nested stacks' and its template, after transforms. What was never
// created is left out; custom resources and types infraharvest doesn't
// know are not importable, what exists only in CloudFormation and nested
// stacks never imported; and each resource of a live stack says what keeps
// it from being retained.
func TestResolveStacks(t *testing.T) {
	scope := resolveScope(t)

	var names []string
	for _, s := range scope.stacks {
		names = append(names, s.name)
	}
	if want := []string{"app", "app-Network-1ABC", "old"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("stacks %v, want %v", names, want)
	}
	app, network, old := scope.stacks[0], scope.stacks[1], scope.stacks[2]
	if network.parent != scopeAppStack || network.region != "eu-west-2" || !old.deleted || app.deleted {
		t.Errorf("network %+v, old %+v", network, old)
	}
	got := map[string]scopedResource{}
	for _, r := range app.resources {
		got[r.logicalID] = *r
	}
	if _, ok := got["Failed"]; ok {
		t.Error("a resource never created is in the scope")
	}
	defaultDelete := []string{"DeletionPolicy is Delete, the default", "UpdateReplacePolicy is Delete, the default"}
	if !reflect.DeepEqual(got["Assets"].retain, defaultDelete) || got["Assets"].notImportable != "" {
		t.Errorf("Assets: %+v", got["Assets"])
	}
	if got["AssetsPolicy"].retain != nil {
		t.Errorf("AssetsPolicy, retained, has %v", got["AssetsPolicy"].retain)
	}
	if r := got["Db"].retain; len(r) != 1 || !strings.Contains(r[0], "Snapshot, the default for its type") {
		t.Errorf("Db: %v", r)
	}
	if r := got["Seed"]; !strings.Contains(r.notImportable, "custom resource") || !reflect.DeepEqual(r.retain, defaultDelete) {
		t.Errorf("Seed: %+v", r)
	}
	if r := got["CDKMetadata"]; r.outcome != report.StackOnlyInCloudFormation || r.retain != nil {
		t.Errorf("CDKMetadata: %+v", r)
	}
	if r := got["Network"]; r.outcome != report.StackNested || r.retain != nil {
		t.Errorf("Network: %+v", r)
	}
	if r := network.resources[0]; r.logicalID != "Role" || r.retain != nil {
		t.Errorf("Role: %+v", r)
	}
	for _, r := range old.resources {
		if r.retain != nil {
			t.Errorf("%s of a deleted stack has its policies checked: %v", r.logicalID, r.retain)
		}
	}

	if regions := scope.Regions(); !reflect.DeepEqual(regions, []string{"eu-west-2"}) {
		t.Errorf("regions %v", regions)
	}
	services := scope.Services(awsCatalogue())
	for _, want := range []string{"s3", "rds", "iam"} {
		if !slices.Contains(services, want) {
			t.Errorf("services %v lack %s", services, want)
		}
	}
	if slices.Contains(services, "cloudformation") {
		t.Errorf("services %v list the stacks themselves", services)
	}
	summary := strings.Join(scope.Summary(), "\n")
	for _, want := range []string{"app (eu-west-2, UPDATE_COMPLETE): 6 resources", "can't be imported: Seed (Custom::Seed)", "3 lack DeletionPolicy and UpdateReplacePolicy Retain", "old (eu-west-2, DELETE_COMPLETE): 2 resources; deleted already"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
}

// A stack that isn't there fails, saying a deleted one is found by its ID.
func TestResolveStacksNotFound(t *testing.T) {
	useFakeAPI(t, scopeStacksAPI(t))

	_, err := ResolveStacks(t.Context(), "default", "", "eu-west-2", []string{"gone"}, awsCatalogue())

	if err == nil || !strings.Contains(err.Error(), "--cfn-stack gone: no such stack") || !strings.Contains(err.Error(), "by its stack ID") {
		t.Errorf("got %v", err)
	}
}

// A stack named as nested in another is warned about, as is one a service
// manages.
func TestResolveStacksWarns(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeStacks":
			return recordQuery(call.Op, recordCfnNS, `<Stacks><member><StackId>arn:aws:cloudformation:eu-west-2:123456789012:stack/awseb-e-abc-stack/4</StackId><StackName>awseb-e-abc-stack</StackName><StackStatus>CREATE_COMPLETE</StackStatus>`+
				`<ParentId>`+scopeAppStack+`</ParentId><CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`)
		case "GetTemplate":
			return recordQuery(call.Op, recordCfnNS, `<TemplateBody>Resources: {}</TemplateBody>`)
		case "ListStackResources":
			return recordQuery(call.Op, recordCfnNS, `<StackResourceSummaries/>`)
		}
		return recordUnexpected(t, call)
	})

	scope, err := ResolveStacks(t.Context(), "default", "", "eu-west-2", []string{"awseb-e-abc-stack"}, awsCatalogue())
	if err != nil {
		t.Fatal(err)
	}

	warnings := strings.Join(scope.stacks[0].warnings, "\n")
	if !strings.Contains(warnings, "Elastic Beanstalk") || !strings.Contains(warnings, "nested in "+scopeAppStack) {
		t.Errorf("warnings %q", warnings)
	}
}

// The scope takes the listed resources of the stacks by physical ID, and
// leaves out the rest: the global call those of every region's stacks.
// A bucket policy, which has its bucket's ID, goes to the stack's policy
// that names the bucket, though its physical ID isn't the bucket's; and a
// deleted stack's retained bucket is taken, not one it deleted. A custom
// resource takes nothing, whatever has its physical ID. Each stack lists
// what each resource matched, with the templates for the report.
func TestImportScope(t *testing.T) {
	scope := resolveScope(t)
	p := &Provider{region: GlobalPseudoRegion}
	p.SetStackScope(scope)

	got, err := p.ImportScope(t.Context(), []harvest.Resource{
		harvest.RecordBare("app-assets", "app-assets", "aws_s3_bucket", "aws"),
		harvest.RecordBare("app-assets", "app-assets", "aws_s3_bucket_policy", "aws"),
		harvest.RecordBare("other", "other", "aws_s3_bucket", "aws"),
		harvest.RecordBare("app-role", "app-role", "aws_iam_role", "aws"),
		harvest.RecordBare("app-db", "app-db", "aws_db_instance", "aws"),
		harvest.RecordBare("old-kept", "old-kept", "aws_s3_bucket", "aws"),
		harvest.RecordBare("old-gone", "old-gone", "aws_s3_bucket", "aws"),
		harvest.RecordBare("seed-1", "seed-1", "aws_lambda_function", "aws"),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantInside := map[string]bool{"aws_s3_bucket app-assets": true, "aws_s3_bucket_policy app-assets": true, "aws_iam_role app-role": true, "aws_db_instance app-db": true, "aws_s3_bucket old-kept": true}
	if !reflect.DeepEqual(got.Inside, wantInside) {
		t.Errorf("inside %v", got.Inside)
	}
	reason := report.OutsideScopePrefix + "not a resource of CloudFormation stacks app, old (--cfn-stack)"
	wantOutside := map[string]string{"aws_s3_bucket other": reason, "aws_s3_bucket old-gone": reason, "aws_lambda_function seed-1": reason}
	if !reflect.DeepEqual(got.Outside, wantOutside) {
		t.Errorf("outside %v", got.Outside)
	}
	matches := map[string][]report.CloudFormationMatch{}
	for _, s := range got.Stacks {
		for _, r := range s.Resources {
			if len(r.Matches) > 0 {
				matches[s.Name+"/"+r.LogicalID] = r.Matches
			}
		}
	}
	wantMatches := map[string][]report.CloudFormationMatch{
		"app/Assets":            {{Type: "aws_s3_bucket", ID: "app-assets"}},
		"app/AssetsPolicy":      {{Type: "aws_s3_bucket_policy", ID: "app-assets"}},
		"app/Db":                {{Type: "aws_db_instance", ID: "app-db"}},
		"app-Network-1ABC/Role": {{Type: "aws_iam_role", ID: "app-role"}},
		"old/Kept":              {{Type: "aws_s3_bucket", ID: "old-kept"}},
	}
	if !reflect.DeepEqual(matches, wantMatches) {
		t.Errorf("matches %v", matches)
	}

	// A regional call of another region takes nothing of them.
	other := &Provider{region: "us-east-1"}
	other.SetStackScope(scope)
	got, err = other.ImportScope(t.Context(), []harvest.Resource{harvest.RecordBare("app-assets", "app-assets", "aws_s3_bucket", "aws")})
	if err != nil || len(got.Inside) != 0 {
		t.Errorf("us-east-1 took %v, %v", got.Inside, err)
	}

	// Without a scope, there is none.
	if got, err := (&Provider{}).ImportScope(t.Context(), nil); got != nil || err != nil {
		t.Errorf("unscoped: %v, %v", got, err)
	}
}

// The report's copy of a template with Retain added keeps the template's
// form: a YAML one its short-form tags and order, a JSON one its keys in
// order, as JSON.
func TestTemplateWithRetain(t *testing.T) {
	yamlTemplate, err := parseTemplate(scopeAppTemplate)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := yamlTemplate.withRetain([]string{"Assets", "Db"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  Assets:\n    Type: AWS::S3::Bucket\n    DeletionPolicy: Retain\n    UpdateReplacePolicy: Retain\n", "Bucket: !Ref Assets", "ServiceToken: !GetAtt SeedFunction.Arn"} {
		if !strings.Contains(patched, want) {
			t.Errorf("patched YAML lacks %q:\n%s", want, patched)
		}
	}
	again, err := parseTemplate(patched)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"Assets", "Db"} {
		if problems := again.retainProblems(id, map[string]string{"Assets": "AWS::S3::Bucket", "Db": "AWS::RDS::DBInstance"}[id]); problems != nil {
			t.Errorf("%s patched still has %v", id, problems)
		}
	}
	if !strings.Contains(again.retainProblems("Seed", "Custom::Seed")[0], "Delete, the default") {
		t.Error("a resource not asked for was patched")
	}

	jsonTemplate, err := parseTemplate(`{"Resources": {"Queue": {"Type": "AWS::SQS::Queue", "DeletionPolicy": "Delete", "Properties": {"DelaySeconds": 5, "FifoQueue": true}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonTemplate.retainProblems("Queue", "AWS::SQS::Queue"); !reflect.DeepEqual(got, []string{"DeletionPolicy is Delete", "UpdateReplacePolicy is Delete, the default"}) {
		t.Errorf("problems %v", got)
	}
	patched, err = jsonTemplate.withRetain([]string{"Queue"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "Resources": {
    "Queue": {
      "Type": "AWS::SQS::Queue",
      "DeletionPolicy": "Retain",
      "Properties": {
        "DelaySeconds": 5,
        "FifoQueue": true
      },
      "UpdateReplacePolicy": "Retain"
    }
  }
}
`
	if patched != want {
		t.Errorf("patched JSON:\n%s\nwant:\n%s", patched, want)
	}
}

// A DB instance in a cluster is deleted by default, not snapshotted, and
// RetainExceptOnCreate retains on deletion; a policy set by a function
// isn't evaluated.
func TestRetainProblems(t *testing.T) {
	template, err := parseTemplate(`Resources:
  Member:
    Type: AWS::RDS::DBInstance
    DeletionPolicy: RetainExceptOnCreate
    UpdateReplacePolicy: Snapshot
    Properties:
      DBClusterIdentifier: !Ref Cluster
  Cluster:
    Type: AWS::RDS::DBCluster
    DeletionPolicy: !If [Prod, Retain, Delete]
    UpdateReplacePolicy: Retain
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := template.retainProblems("Member", "AWS::RDS::DBInstance"); !reflect.DeepEqual(got, []string{"UpdateReplacePolicy is Snapshot"}) {
		t.Errorf("Member: %v", got)
	}
	if got := template.retainProblems("Cluster", "AWS::RDS::DBCluster"); !reflect.DeepEqual(got, []string{"DeletionPolicy is set by a function infraharvest doesn't evaluate"}) {
		t.Errorf("Cluster: %v", got)
	}
	if got := template.retainProblems("Missing", "AWS::S3::Bucket"); len(got) != 1 || !strings.Contains(got[0], "not in the stack's template") {
		t.Errorf("Missing: %v", got)
	}
	if !snapshotByDefault("AWS::RDS::DBCluster", nil) || snapshotByDefault("AWS::RDS::DBInstance", template.resource("Member")) || snapshotByDefault("AWS::Redshift::Cluster", nil) {
		t.Error("snapshotByDefault")
	}
}

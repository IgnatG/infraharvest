// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || aws)

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/report"
)

// cfnStackWait is how long the test waits for a stack to update or be
// deleted.
const cfnStackWait = 20 * time.Minute

// TestSandboxAWSCloudFormation creates testdata/sandbox/aws-cfn in a real
// AWS account as the fixture profile, deletes its stack old, which retains
// its bucket, and moves both stacks to Terraform as a user would, imports
// as the read-only profile, the stack's changes as the fixture profile:
//
//  1. It imports both with --cfn-stack, app by name and old by its stack
//     ID, acknowledging app's custom resource with --cfn-ignore. Every root
//     must plan as imports with no changes; app must be reported not safe to
//     delete, as its resources aren't retained, and old as deleted, its
//     bucket imported.
//  2. It updates app with the copy of its template that has Retain added,
//     the nested stack's copy uploaded over its template first.
//  3. It imports again: app must be safe to delete, its custom resource
//     acknowledged, and every root must plan as imports with no changes.
//  4. It deletes app. The roots of the second import must still plan as
//     imports with no changes: every resource survived.
//
// What the stacks retain is outside the estate's state, so the test
// deletes it, by name, every name the estate's (ih-sbx-...), whatever
// happened; the estate's destroy deletes the rest. The sandbox-aws
// workflow runs it when asked to (estate=cfn); see e2e/README.md.
func TestSandboxAWSCloudFormation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	s := newSandbox(ctx, t)
	fixture, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(s.fixtureProfile), awsconfig.WithRegion(s.region))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := sts.NewFromConfig(fixture).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatal(err)
	}
	names := cfnSandboxNamesFor(s.engineName, aws.ToString(identity.Account))

	estate := s.seed(ctx, t, "aws-cfn", "suffix="+s.engineName)
	// After the estate's destroy is registered, so that it runs first.
	t.Cleanup(func() { deleteCFNSandbox(t, fixture, names) })
	state := estate.apply(ctx, t)
	stackIDs := map[string]string{}
	templates := ""
	for _, r := range state {
		switch r.Type {
		case "aws_cloudformation_stack":
			stackIDs[r.Name], _ = r.AttributeValues["id"].(string)
		case "aws_s3_bucket":
			templates, _ = r.AttributeValues["id"].(string)
		}
	}
	cfn := cloudformation.NewFromConfig(fixture)
	if _, err := cfn.DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: aws.String(stackIDs["old"])}); err != nil {
		t.Fatal(err)
	}
	if err := cloudformation.NewStackDeleteCompleteWaiter(cfn).Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackIDs["old"])}, cfnStackWait); err != nil {
		t.Fatal(err)
	}

	importStacks := func(out string) report.Report {
		t.Helper()
		t.Setenv("AWS_PROFILE", s.readonlyProfile)
		root := cli.NewCmdRoot()
		root.SetArgs([]string{
			"import", "aws",
			"--engine=" + s.engineName,
			"--terraform-path=" + s.execPath,
			"--profile=" + s.readonlyProfile,
			"--regions=" + s.region,
			"--cfn-stack=" + names.app,
			"--cfn-stack=" + stackIDs["old"],
			"--cfn-ignore=" + names.app + "/Seed",
			"--out=" + out,
		})
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("infraharvest import aws --cfn-stack: %v", err)
		}
		coverage := readCoverage(t, out)
		for _, dir := range generatedDirs(t, out) {
			checkNoChanges(ctx, t, dir, s.execPath, s.plugins, secretValues(t, dir, nil))
		}
		return coverage
	}

	// 1. Nothing is retained yet.
	first := filepath.Join(s.out, "first")
	coverage := importStacks(first)
	app, old := cfnStack(t, coverage, names.app), cfnStack(t, coverage, names.old)
	if app.SafeToDelete || !strings.Contains(strings.Join(app.Blockers, "\n"), "Assets (AWS::S3::Bucket) isn't retained") {
		t.Errorf("app before Retain: safe %v, blockers %v", app.SafeToDelete, app.Blockers)
	}
	if !old.Deleted || len(old.Resources) != 1 || old.Resources[0].Outcome != report.StackImported {
		t.Errorf("old: %+v", old)
	}
	checkCFNOutcomes(t, coverage, names.app)

	// 2. Retain everything, the nested stack's template first: the parent
	// reads it from the bucket on update.
	nested := cfnStack(t, coverage, names.app+"-Network-")
	if app.RetainTemplate == "" || nested.RetainTemplate == "" {
		t.Fatalf("no copy with Retain: app %q, nested %q", app.RetainTemplate, nested.RetainTemplate)
	}
	nestedBody := readFile(t, filepath.Join(first, filepath.FromSlash(nested.RetainTemplate)))
	if _, err := s3.NewFromConfig(fixture).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(templates), Key: aws.String("network.yaml"), Body: strings.NewReader(nestedBody)}); err != nil {
		t.Fatal(err)
	}
	if _, err := cfn.UpdateStack(ctx, &cloudformation.UpdateStackInput{
		StackName:    aws.String(names.app),
		TemplateBody: aws.String(readFile(t, filepath.Join(first, filepath.FromSlash(app.RetainTemplate)))),
		Capabilities: []cftypes.Capability{cftypes.CapabilityCapabilityNamedIam},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cloudformation.NewStackUpdateCompleteWaiter(cfn).Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(names.app)}, cfnStackWait); err != nil {
		t.Fatal(err)
	}

	// 3. Safe to delete, but for nothing: the custom resource is
	// acknowledged.
	second := filepath.Join(s.out, "second")
	coverage = importStacks(second)
	app = cfnStack(t, coverage, names.app)
	if !app.SafeToDelete {
		t.Errorf("app after Retain isn't safe to delete: %v", app.Blockers)
	}
	for _, r := range app.Resources {
		if r.LogicalID == "Seed" && r.Outcome != report.StackAcknowledged {
			t.Errorf("Seed: %+v", r)
		}
	}
	checkCFNOutcomes(t, coverage, names.app)

	// 4. Every resource outlives the stack.
	if _, err := cfn.DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: aws.String(stackIDs["app"])}); err != nil {
		t.Fatal(err)
	}
	if err := cloudformation.NewStackDeleteCompleteWaiter(cfn).Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackIDs["app"])}, cfnStackWait); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", s.readonlyProfile)
	for _, dir := range generatedDirs(t, second) {
		checkNoChanges(ctx, t, dir, s.execPath, s.plugins, secretValues(t, dir, nil))
	}
}

// cfnSandboxNames are the names testdata/sandbox/aws-cfn gives what it
// creates, for the engine suffix in account.
type cfnSandboxNames struct {
	app, old, account            string
	buckets                      []string
	queue, topic, role, function string
}

func cfnSandboxNamesFor(suffix, account string) cfnSandboxNames {
	name := "ih-sbx-" + suffix + "-cfn"
	return cfnSandboxNames{
		app:      name,
		account:  account,
		old:      name + "-old",
		buckets:  []string{name + "-" + account + "-assets", name + "-" + account + "-kept"},
		queue:    name + "-jobs",
		topic:    name + "-alerts",
		role:     name + "-seed",
		function: name + "-seed",
	}
}

// cfnStack returns the stack of coverage whose name is name, or starts
// with it where it ends with a dash, as a nested stack's does.
func cfnStack(t *testing.T, coverage report.Report, name string) report.CloudFormationStack {
	t.Helper()
	for _, s := range coverage.CloudFormationStacks {
		if s.Name == name || (strings.HasSuffix(name, "-") && strings.HasPrefix(s.Name, name)) {
			return s
		}
	}
	t.Fatalf("no stack %s in the report: %+v", name, coverage.CloudFormationStacks)
	return report.CloudFormationStack{}
}

// checkCFNOutcomes checks what became of the resources of app and of its
// nested stack: the bucket, the queue and its policy, the role, the
// function and the topic imported, the custom resource not.
func checkCFNOutcomes(t *testing.T, coverage report.Report, app string) {
	t.Helper()
	want := map[string]string{
		"Assets": report.StackImported, "Jobs": report.StackImported, "JobsPolicy": report.StackImported,
		"Role": report.StackImported, "SeedFunction": report.StackImported, "Alerts": report.StackImported,
		"Network": report.StackNested,
	}
	for _, s := range coverage.CloudFormationStacks {
		if s.Name != app && s.Parent == "" {
			continue
		}
		for _, r := range s.Resources {
			if outcome, ok := want[r.LogicalID]; ok && r.Outcome != outcome {
				t.Errorf("%s/%s (%s): %s, want %s: %s", s.Name, r.LogicalID, r.Type, r.Outcome, outcome, r.Reason)
			}
		}
	}
}

// deleteCFNSandbox deletes what testdata/sandbox/aws-cfn's stacks hold
// that the estate's destroy wouldn't: the stacks, if still there, then
// what they retained, by its name. What is already gone is fine.
func deleteCFNSandbox(t *testing.T, config aws.Config, names cfnSandboxNames) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cfn := cloudformation.NewFromConfig(config)
	for _, stack := range []string{names.app, names.old} {
		if _, err := cfn.DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: aws.String(stack)}); err != nil {
			t.Logf("delete stack %s: %v", stack, err)
			continue
		}
		if err := cloudformation.NewStackDeleteCompleteWaiter(cfn).Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stack)}, cfnStackWait); err != nil && !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("delete stack %s: %v", stack, err)
		}
	}
	gone := func(what string, err error) {
		if err != nil && !strings.Contains(err.Error(), "NotFound") && !strings.Contains(err.Error(), "NoSuch") && !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("delete %s: %v", what, err)
		}
	}
	bucketClient := s3.NewFromConfig(config)
	for _, bucket := range names.buckets {
		gone("bucket "+bucket, emptyBucket(ctx, bucketClient, bucket))
		_, err := bucketClient.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
		gone("bucket "+bucket, err)
	}
	queues := sqs.NewFromConfig(config)
	if url, err := queues.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(names.queue)}); err == nil {
		_, err = queues.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url.QueueUrl})
		gone("queue "+names.queue, err)
	}
	topics := sns.NewFromConfig(config)
	topicARN := "arn:aws:sns:" + config.Region + ":" + names.account + ":" + names.topic
	_, err := topics.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	gone("topic "+names.topic, err)
	_, err = lambda.NewFromConfig(config).DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(names.function)})
	gone("function "+names.function, err)
	_, err = cloudwatchlogs.NewFromConfig(config).DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String("/aws/lambda/" + names.function)})
	gone("log group of "+names.function, err)
	roles := iam.NewFromConfig(config)
	if attached, err := roles.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(names.role)}); err == nil {
		for _, p := range attached.AttachedPolicies {
			_, err := roles.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(names.role), PolicyArn: p.PolicyArn})
			gone("policy of role "+names.role, err)
		}
	}
	_, err = roles.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(names.role)})
	gone("role "+names.role, err)
}

// emptyBucket deletes every object version and delete marker of bucket.
func emptyBucket(ctx context.Context, client *s3.Client, bucket string) error {
	for {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
		if err != nil {
			return err
		}
		var objects []s3types.ObjectIdentifier
		for _, v := range out.Versions {
			objects = append(objects, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range out.DeleteMarkers {
			objects = append(objects, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(objects) == 0 {
			return nil
		}
		if _, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: objects}}); err != nil {
			return err
		}
	}
}

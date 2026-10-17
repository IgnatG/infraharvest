// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// checkNoLongerInCloud checks what --incremental says of a resource a
// root has that has been deleted since: a queue created after out, the
// output of checkIncremental, is added to its root, then deleted, and the
// import after that, which adds another new queue, reports it no longer
// in the cloud, in coverage.json and the root's README, without changing
// what the root has. The queues are deleted when the test ends, so the
// emulator has testdata/aws again.
func checkNoLongerInCloud(ctx context.Context, t *testing.T, engineName, execPath, selectionFile, out string) {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	svc := sqs.NewFromConfig(cfg)

	goneURL := createQueue(ctx, t, svc, "e2e-incremental-gone")
	importAWS(ctx, t, engineName, execPath, "--selection="+selectionFile, "--incremental", "--out="+out)
	added := coverageOf(t, out)
	if len(added.NotInCloud) != 0 {
		t.Errorf("--incremental reports what the emulator has as no longer in the cloud: %+v", added.NotInCloud)
	}

	if _, err := svc.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: awssdk.String(goneURL)}); err != nil {
		t.Fatalf("delete queue %s: %v", goneURL, err)
	}
	createQueue(ctx, t, svc, "e2e-incremental-new")
	before := outputFiles(t, out)
	importAWS(ctx, t, engineName, execPath, "--selection="+selectionFile, "--incremental", "--out="+out)
	after := outputFiles(t, out)
	if len(addedConfiguration(t, before, after)) == 0 {
		t.Error("--incremental didn't add the new queue")
	}

	coverage := coverageOf(t, out)
	if len(coverage.NotInCloud) != 1 {
		t.Fatalf("--incremental after deleting a queue the root has: no longer in the cloud %+v, want the queue", coverage.NotInCloud)
	}
	gone := coverage.NotInCloud[0]
	if !strings.HasPrefix(gone.Address, "aws_sqs_queue.") || !strings.HasSuffix(gone.ID, "/e2e-incremental-gone") || gone.Scope == "" {
		t.Errorf("no longer in the cloud: got %+v, want the deleted queue", gone)
	}
	// What is gone is for people to see to, not a failure.
	if len(coverage.Failures) != 0 || coverage.ExitCode != added.ExitCode {
		t.Errorf("a queue gone changes the outcome: exit code %d, was %d; failures %v", coverage.ExitCode, added.ExitCode, coverage.Failures)
	}
	readme := after[path.Join(gone.Path, engine.ReadmeFileName)]
	if !strings.Contains(readme, "## No longer in the cloud") || !strings.Contains(readme, "- `"+gone.Address+"` (`"+gone.ID+"`)") {
		t.Errorf("the README of %s doesn't list %s as no longer in the cloud:\n%s", gone.Path, gone.Address, readme)
	}
	if md := after[report.Dir+"/report.md"]; !strings.Contains(md, "## No longer in the cloud") || !strings.Contains(md, "`"+gone.Address+"`") {
		t.Errorf("report.md doesn't list %s as no longer in the cloud:\n%s", gone.Address, md)
	}
}

// createQueue creates the queue name in the emulator and returns its URL.
// It is deleted when the test ends, if it is still there.
func createQueue(ctx context.Context, t *testing.T, svc *sqs.Client, name string) string {
	t.Helper()
	created, err := svc.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: awssdk.String(name)})
	if err != nil {
		t.Fatalf("create queue %s: %v", name, err)
	}
	url := awssdk.ToString(created.QueueUrl)
	t.Cleanup(func() {
		// The test deletes some itself.
		_, _ = svc.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: awssdk.String(url)})
	})
	return url
}

// coverageOf returns the coverage.json of the import into out.
func coverageOf(t *testing.T, out string) report.Report {
	t.Helper()
	var coverage report.Report
	if err := json.Unmarshal([]byte(outputFiles(t, out)[report.Dir+"/coverage.json"]), &coverage); err != nil {
		t.Fatal(err)
	}
	return coverage
}

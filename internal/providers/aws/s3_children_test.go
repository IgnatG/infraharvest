// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// s3Answers are fake S3 answers by query parameter (?versioning, ...):
// configured, not configured, or failing.
var s3Answers = map[string]string{
	"versioning":        `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`,
	"encryption":        `<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`,
	"lifecycle":         `<Error><Code>NoSuchLifecycleConfiguration</Code></Error>`,
	"cors":              `<Error><Code>NoSuchCORSConfiguration</Code></Error>`,
	"website":           `<Error><Code>NoSuchWebsiteConfiguration</Code></Error>`,
	"logging":           `<BucketLoggingStatus></BucketLoggingStatus>`,
	"publicAccessBlock": `<PublicAccessBlockConfiguration><BlockPublicAcls>true</BlockPublicAcls></PublicAccessBlockConfiguration>`,
	"ownershipControls": `<OwnershipControls><Rule><ObjectOwnership>BucketOwnerEnforced</ObjectOwnership></Rule></OwnershipControls>`,
	"accelerate":        `<AccelerateConfiguration></AccelerateConfiguration>`,
	"requestPayment":    `<RequestPaymentConfiguration><Payer>BucketOwner</Payer></RequestPaymentConfiguration>`,
	"object-lock":       `<Error><Code>NotImplemented</Code></Error>`,
	"replication":       `<Error><Code>ReplicationConfigurationNotFoundError</Code></Error>`,
}

func s3Answer(call apiCall) string {
	for param, answer := range s3Answers {
		if call.Query.Has(param) {
			return answer
		}
	}
	return `<Error><Code>Unexpected</Code></Error>`
}

func TestChildImports(t *testing.T) {
	useFakeAPI(t, s3Answer)
	p := &Provider{region: "us-east-1"}
	bucket := harvest.RecordBare("artifacts", "artifacts", "aws_s3_bucket", "aws")

	children, err := p.ChildImports(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, c := range children {
		if c.Observed.ID != "artifacts" {
			t.Errorf("%s imports %q, want the bucket name", c.Address.Type, c.Observed.ID)
		}
		got = append(got, c.Address.Type)
	}
	want := []string{"aws_s3_bucket_versioning", "aws_s3_bucket_server_side_encryption_configuration", "aws_s3_bucket_public_access_block", "aws_s3_bucket_ownership_controls"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestChildImportsReportsErrors(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Query.Has("versioning") {
			return `<Error><Code>AccessDenied</Code></Error>`
		}
		return s3Answer(call)
	})
	p := &Provider{region: "us-east-1"}

	children, err := p.ChildImports(t.Context(), harvest.RecordBare("artifacts", "artifacts", "aws_s3_bucket", "aws"))

	if err == nil || !strings.Contains(err.Error(), "aws_s3_bucket_versioning") {
		t.Errorf("want the versioning error, got %v", err)
	}
	if len(children) != 3 {
		t.Errorf("want the other children still listed, got %d", len(children))
	}
}

func TestChildImportsOnlyForBuckets(t *testing.T) {
	p := &Provider{region: "us-east-1"}
	children, err := p.ChildImports(t.Context(), harvest.RecordBare("q", "q", "aws_sqs_queue", "aws"))
	if err != nil || children != nil {
		t.Errorf("got %v, %v; want none", children, err)
	}
}

// Every child the catalogue names for a bucket has a probe, and every
// probe is for one of them; the bucket leaves out the arguments the
// children carry.
func TestS3ProbesAreTheCataloguedChildren(t *testing.T) {
	entry, ok := awsCatalogue().Entry("aws_s3_bucket")
	if !ok {
		t.Fatal("aws_s3_bucket isn't in the catalogue")
	}
	for _, child := range entry.Children {
		if _, ok := s3Probes[child]; !ok {
			t.Errorf("%s has no probe", child)
		}
	}
	for child := range s3Probes {
		if !slices.Contains(entry.Children, child) {
			t.Errorf("%s isn't a catalogued child of aws_s3_bucket", child)
		}
	}
	for _, name := range []string{"versioning", "server_side_encryption_configuration", "policy", "acl"} {
		if !slices.Contains(entry.Omit, name) {
			t.Errorf("%s not omitted from aws_s3_bucket", name)
		}
	}
}

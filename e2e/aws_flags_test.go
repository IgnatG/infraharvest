// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// localAWS skips the test without an AWS emulator, fails it for any other
// endpoint, and points the AWS SDK at the emulator with no real
// credentials.
func localAWS(t *testing.T) {
	t.Helper()
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)
}

// accountConfig is the SDK configuration of the emulator's account, which
// the emulator takes from a 12-digit access key ID.
func accountConfig(ctx context.Context, t *testing.T, account string) awssdk.Config {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(account, "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// discoverAWSSQS runs discover for the SQS queues of us-east-1 with args,
// as the account whose access key is the process's, and returns the
// selection file it writes, what it logged and its error.
func discoverAWSSQS(ctx context.Context, t *testing.T, args ...string) (*selection.File, string, error) {
	t.Helper()
	path := t.TempDir() + "/selection.yaml"
	var err error
	logged := logDuring(func() {
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{"discover", "aws", "--regions=us-east-1", "--services=sqs", "--selection=" + path}, args...))
		err = root.ExecuteContext(ctx)
	})
	if err != nil {
		return nil, logged, err
	}
	f, loadErr := selection.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return f, logged, nil
}

// TestAWSOrganization checks --organization, which imports every active
// account of the organization through the role --assume-role names. The
// emulator's account has no organization at first, which the command
// refuses saying so. Once the account, the organization's management
// account has one with two member accounts, each with a queue, and every
// account of it, the management account too, has the default role,
// discover lists the queue of each member, found through its role.
func TestAWSOrganization(t *testing.T) {
	localAWS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	const management = "777777777777"
	t.Setenv("AWS_ACCESS_KEY_ID", management)

	_, _, err := discoverAWSSQS(ctx, t, "--organization")
	if err == nil || !strings.Contains(err.Error(), "list the organization's accounts") {
		t.Fatalf("--organization of an account with no organization: %v, want it to say it can't list the organization's accounts", err)
	}

	orgs := organizations.NewFromConfig(accountConfig(ctx, t, management))
	if _, err := orgs.CreateOrganization(ctx, &organizations.CreateOrganizationInput{}); err != nil {
		t.Fatalf("create the organization: %v", err)
	}
	var members []string
	t.Cleanup(func() {
		// Not the test's ctx, which the cleanup may outlive. The emulator
		// is shared with the tests that follow: leave it without an
		// organization.
		ctx := context.Background()
		for _, id := range members {
			_, _ = orgs.CloseAccount(ctx, &organizations.CloseAccountInput{AccountId: awssdk.String(id)})
			_, _ = orgs.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: awssdk.String(id)})
		}
		_, _ = orgs.DeleteOrganization(ctx, &organizations.DeleteOrganizationInput{})
	})
	trust := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"sts:AssumeRole"}]}`, management)
	// The role --assume-role names by default, in each account of the
	// organization, the management account's too.
	role := func(account string) {
		cfg := accountConfig(ctx, t, account)
		if _, err := iam.NewFromConfig(cfg).CreateRole(ctx, &iam.CreateRoleInput{RoleName: awssdk.String("infraharvest-readonly"), AssumeRolePolicyDocument: awssdk.String(trust)}); err != nil {
			t.Fatalf("create the role of account %s: %v", account, err)
		}
		t.Cleanup(func() {
			_, _ = iam.NewFromConfig(cfg).DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: awssdk.String("infraharvest-readonly")})
		})
	}
	role(management)
	queues := map[string]string{}
	for _, name := range []string{"first", "second"} {
		created, err := orgs.CreateAccount(ctx, &organizations.CreateAccountInput{
			AccountName: awssdk.String("e2e-" + name),
			Email:       awssdk.String("e2e-" + name + "@example.com"),
		})
		if err != nil || created.CreateAccountStatus == nil || awssdk.ToString(created.CreateAccountStatus.AccountId) == "" {
			t.Fatalf("create the account %s: %v (%+v)", name, err, created)
		}
		account := awssdk.ToString(created.CreateAccountStatus.AccountId)
		members = append(members, account)
		role(account)
		queues[account] = createQueue(ctx, t, sqs.NewFromConfig(accountConfig(ctx, t, account)), "e2e-organization-"+name)
	}

	f, logged, err := discoverAWSSQS(ctx, t, "--organization")
	if err != nil {
		t.Fatalf("infraharvest discover aws --organization: %v\n%s", err, logged)
	}
	for account, url := range queues {
		found := false
		for _, r := range f.Resources {
			if r.Type == "aws_sqs_queue" && r.ID == url {
				found = true
				if !r.Include || !strings.Contains(r.Scope, account) {
					t.Errorf("queue %s: include=%v scope %q, want it included, in account %s", url, r.Include, r.Scope, account)
				}
			}
		}
		if !found {
			t.Errorf("--organization lists no queue %s of account %s", url, account)
		}
	}
}

// TestAWSResourceExplorer checks --resource-explorer against the
// emulator, whose Resource Explorer has an index and a default view, which
// are new: an index younger than a day may not have found every resource,
// so the import warns it can't rule any service out and lists every one,
// where it would leave out a service the index shows nothing of. The queue
// the emulator has is listed with the flag and without it, and only the run
// with the flag says anything of Resource Explorer.
func TestAWSResourceExplorer(t *testing.T) {
	localAWS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	const account = "888888888888"
	t.Setenv("AWS_ACCESS_KEY_ID", account)
	url := createQueue(ctx, t, sqs.NewFromConfig(accountConfig(ctx, t, account)), "e2e-resource-explorer")

	listed := func(f *selection.File) bool {
		for _, r := range f.Resources {
			if r.Type == "aws_sqs_queue" && r.ID == url {
				return r.Include
			}
		}
		return false
	}

	f, logged, err := discoverAWSSQS(ctx, t)
	if err != nil {
		t.Fatal(err)
	}
	if !listed(f) {
		t.Errorf("discover lists no queue %s", url)
	}
	if strings.Contains(logged, "Resource Explorer") {
		t.Errorf("discover without --resource-explorer logs about Resource Explorer:\n%s", logged)
	}

	f, logged, err = discoverAWSSQS(ctx, t, "--resource-explorer")
	if err != nil {
		t.Fatalf("infraharvest discover aws --resource-explorer: %v\n%s", err, logged)
	}
	if !listed(f) {
		t.Errorf("--resource-explorer leaves out queue %s, which the index is too new to rule out", url)
	}
	if !strings.Contains(logged, "Resource Explorer can't rule out services in us-east-1") || !strings.Contains(logged, "less than a day ago") {
		t.Errorf("--resource-explorer: the log doesn't say the new index rules nothing out:\n%s", logged)
	}
}

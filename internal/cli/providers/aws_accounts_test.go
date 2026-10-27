// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

func TestAWSAccounts(t *testing.T) {
	ctx := context.Background()
	accounts, err := awsAccounts(ctx, importer.Options{Accounts: []string{"111122223333", "444455556666"}, AssumeRole: DefaultAssumeRole})
	if err != nil || !reflect.DeepEqual(accounts, []string{"111122223333", "444455556666"}) {
		t.Errorf("accounts: %v, %v", accounts, err)
	}
	if accounts, err := awsAccounts(ctx, importer.Options{AssumeRole: DefaultAssumeRole}); err != nil || accounts != nil {
		t.Errorf("one account: %v, %v", accounts, err)
	}
	if _, err := awsAccounts(ctx, importer.Options{Accounts: []string{"111122223333"}, Organization: true, AssumeRole: DefaultAssumeRole}); err == nil {
		t.Error("both: want an error")
	}
	for name, options := range map[string]importer.Options{
		"a role for one account":                     {Accounts: []string{"111122223333"}, AssumeRole: "arn:aws:iam::111122223333:role/x"},
		"a role for one account of the organization": {Organization: true, AssumeRole: "arn:aws:iam::111122223333:role/x"},
	} {
		if _, err := awsAccounts(ctx, options); err == nil || !strings.Contains(err.Error(), "{account}") {
			t.Errorf("%s: want an error about {account}, got %v", name, err)
		}
	}
}

// An account that can't be imported is reported, and the run goes on to
// the next; an interrupt stops it.
func TestImportAWSAccountsGoesOn(t *testing.T) {
	ctx := context.Background()
	options := importer.Options{AssumeRole: DefaultAssumeRole, Out: t.TempDir()}
	var roles []string
	importAccount := func(_ context.Context, o importer.Options) error {
		roles = append(roles, o.RoleARN)
		if strings.Contains(o.RoleARN, "111122223333") {
			return errors.New("AccessDenied: can't assume the role")
		}
		return nil
	}

	run := importer.NewRun()
	err := run.Do(func() error {
		return importAWSAccounts(ctx, run, options, []string{"111122223333", "444455556666"}, importAccount)
	})

	want := []string{"arn:aws:iam::111122223333:role/infraharvest-readonly", "arn:aws:iam::444455556666:role/infraharvest-readonly"}
	if !reflect.DeepEqual(roles, want) {
		t.Errorf("roles %v, want %v: the run must go on", roles, want)
	}
	if code := importer.ExitCode(err); code != report.ExitIncomplete {
		t.Errorf("exit code %d, want %d (%v)", code, report.ExitIncomplete, err)
	}
	if err == nil || !strings.Contains(err.Error(), "account 111122223333: AccessDenied") {
		t.Errorf("failure not reported: %v", err)
	}

	roles = nil
	run = importer.NewRun()
	err = run.Do(func() error {
		return importAWSAccounts(ctx, run, options, []string{"111122223333", "444455556666"}, func(context.Context, importer.Options) error { return context.Canceled })
	})
	if !errors.Is(err, context.Canceled) || len(roles) != 0 {
		t.Errorf("interrupted: %v", err)
	}
}

func TestGroupAWSResources(t *testing.T) {
	global, eastOnly, regional := groupAWSServices(importer.Options{Services: []string{"*"}, Skip: []string{"iam"}})

	all := slices.Concat(global, eastOnly, regional)
	if slices.Contains(all, "*") {
		t.Errorf("* not expanded: %v", regional)
	}
	if !slices.Contains(global, "route53") || !slices.Contains(global, "cloudfront") || slices.Contains(regional, "route53") {
		t.Errorf("global services: %v", global)
	}
	if !slices.Contains(eastOnly, "wafv2_cloudfront") || !slices.Contains(regional, "vpc") {
		t.Errorf("east-only %v, regional %v", eastOnly, regional)
	}
	if slices.Contains(all, "iam") {
		t.Error("iam not excluded")
	}
	if want := len(importer.Services(newAWSProvider())) - 1; len(all) != want {
		t.Errorf("%d services, want %d", len(all), want)
	}

	global, _, regional = groupAWSServices(importer.Options{Services: []string{"vpc", "iam"}})
	if !slices.Equal(global, []string{"iam"}) || !slices.Equal(regional, []string{"vpc"}) {
		t.Errorf("global %v, regional %v", global, regional)
	}
}

// importAWSAccounts imports accounts as the aws command plans them, with
// importAccount in place of each Import call, in order, stopping at the
// first that fails.
func importAWSAccounts(ctx context.Context, run *importer.Run, options importer.Options, accounts []string, importAccount func(context.Context, importer.Options) error) error {
	options.Accounts = accounts
	p, err := planAWS(ctx, options)
	if err != nil {
		return err
	}
	return p.run(ctx, run, options, newAWSProvider, func(ctx context.Context, _ *importer.Run, calls []importer.Call) error {
		for _, c := range calls {
			if c.Provider == nil {
				return errors.New("a call without a provider")
			}
			if err := importAccount(ctx, c.Options); err != nil {
				return err
			}
		}
		return nil
	})
}

// Each account, region and group of services is one Import call, whose
// arguments are the region, the profile and the role.
func TestAWSCalls(t *testing.T) {
	type want struct {
		args      []string
		resources []string
		regions   []string
		layout    string
	}
	check := func(name string, calls []call, wants []want) {
		t.Helper()
		if len(calls) != len(wants) {
			t.Fatalf("%s: %d calls, want %d: %+v", name, len(calls), len(wants), calls)
		}
		for i, w := range wants {
			c := calls[i]
			if !slices.Equal(c.args, w.args) || !slices.Equal(c.options.Services, w.resources) || !slices.Equal(c.options.Regions, w.regions) || c.options.Layout != w.layout {
				t.Errorf("%s: call %d: args %q, resources %v, regions %v, layout %q; want %+v", name, i, c.args, c.options.Services, c.options.Regions, c.options.Layout, w)
			}
		}
	}

	byAccount := "{out}/{provider}/{account}/"
	options := importer.Options{Profile: "prod", RoleARN: "arn:aws:iam::111122223333:role/x", Layout: byAccount, Services: []string{"vpc", "iam", "wafv2_cloudfront"}}
	check("no regions", awsCalls(options), []want{
		{[]string{"", "prod", options.RoleARN}, options.Services, nil, byAccount},
	})

	options.Regions = []string{"eu-west-1", "eu-west-2"}
	check("regions", awsCalls(options), []want{
		{[]string{"aws-global", "prod", options.RoleARN}, []string{"iam"}, []string{"aws-global"}, byAccount + "global/"},
		{[]string{"us-east-1", "prod", options.RoleARN}, []string{"wafv2_cloudfront"}, []string{"us-east-1"}, byAccount + "us-east-1/"},
		{[]string{"eu-west-1", "prod", options.RoleARN}, []string{"vpc"}, options.Regions, byAccount + "eu-west-1/"},
		{[]string{"eu-west-2", "prod", options.RoleARN}, []string{"vpc"}, options.Regions, byAccount + "eu-west-2/"},
	})
	// The global and us-east-1-only calls go first, one at a time; the
	// regions' then run at once.
	var first []bool
	for _, c := range awsCalls(options) {
		first = append(first, c.first)
	}
	if want := []bool{true, true, false, false}; !slices.Equal(first, want) {
		t.Errorf("first: got %v, want %v", first, want)
	}

	// Global services, and those only in us-east-1, have a root of their
	// own beside a single region's.
	options.Regions = []string{"eu-west-1"}
	check("one region and global services", awsCalls(options), []want{
		{[]string{"aws-global", "prod", options.RoleARN}, []string{"iam"}, []string{"aws-global"}, byAccount + "global/"},
		{[]string{"us-east-1", "prod", options.RoleARN}, []string{"wafv2_cloudfront"}, []string{"us-east-1"}, byAccount + "us-east-1/"},
		{[]string{"eu-west-1", "prod", options.RoleARN}, []string{"vpc"}, options.Regions, byAccount + "eu-west-1/"},
	})

	// In us-east-1, those only there are imported with the regional ones.
	options.Regions = []string{"us-east-1", "eu-west-1"}
	check("us-east-1 among the regions", awsCalls(options), []want{
		{[]string{"aws-global", "prod", options.RoleARN}, []string{"iam"}, []string{"aws-global"}, byAccount + "global/"},
		{[]string{"us-east-1", "prod", options.RoleARN}, []string{"vpc", "wafv2_cloudfront"}, options.Regions, byAccount + "us-east-1/"},
		{[]string{"eu-west-1", "prod", options.RoleARN}, []string{"vpc"}, options.Regions, byAccount + "eu-west-1/"},
	})

	options.Regions = []string{"eu-west-1"}
	options.Services = []string{"vpc"}
	check("one region, no global services", awsCalls(options), []want{
		{[]string{"eu-west-1", "prod", options.RoleARN}, []string{"vpc"}, options.Regions, byAccount},
	})

	options.Services = []string{"vpc", "iam"}
	options.Layout = importer.DefaultLayout
	check("a pattern with {region}", awsCalls(options), []want{
		{[]string{"aws-global", "prod", options.RoleARN}, []string{"iam"}, []string{"aws-global"}, importer.DefaultLayout},
		{[]string{"eu-west-1", "prod", options.RoleARN}, []string{"vpc"}, options.Regions, importer.DefaultLayout},
	})
}

// Without --accounts or --organization, a role ARN without {account} is
// assumed as it is, and the default role isn't assumed at all.
func TestAWSSingleAccountRole(t *testing.T) {
	ctx := context.Background()
	for role, want := range map[string]string{
		DefaultAssumeRole:                  "",
		"arn:aws:iam::111122223333:role/x": "arn:aws:iam::111122223333:role/x",
	} {
		p, err := planAWS(ctx, importer.Options{AssumeRole: role})
		if err != nil {
			t.Fatal(err)
		}
		if p.scopes != nil {
			t.Errorf("%s: scopes %v, want none", role, p.scopes)
		}
		calls := p.calls(importer.Options{AssumeRole: role}, "")
		if len(calls) != 1 || calls[0].options.RoleARN != want || calls[0].args[2] != want {
			t.Errorf("%s: calls %+v, want one with role %q", role, calls, want)
		}
	}
}

// --cfn-stack reaches the one account the credentials reach, and a stack
// name is looked for in one region: both are refused before any stack is
// read.
func TestPlanAWSStacksRefuses(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		options importer.Options
		want    string
	}{
		"with --accounts":           {importer.Options{CloudFormationStacks: []string{"app"}, Accounts: []string{"111122223333"}, AssumeRole: DefaultAssumeRole}, "without --accounts or --organization"},
		"a name in several regions": {importer.Options{CloudFormationStacks: []string{"app"}, Regions: []string{"eu-west-1", "eu-west-2"}, AssumeRole: DefaultAssumeRole}, "--cfn-stack app: a stack name is looked for in one region"},
	} {
		if _, err := planAWS(ctx, c.options); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
}

// A configuration file sets --cfn-stack and --cfn-ignore, one value per
// item of a list, as the command line takes them.
func TestCloudFormationFlagsFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nproviders:\n  aws:\n    cfn-stack: [app, \"arn:aws:cloudformation:eu-west-2:111111111111:stack/old/1\"]\n    cfn-ignore: [app/Seed]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := Command("aws", importer.Options{})
	if err := f.Apply(cmd.PersistentFlags(), "aws"); err != nil {
		t.Fatal(err)
	}
	stacks, _ := cmd.PersistentFlags().GetStringArray("cfn-stack")
	ignore, _ := cmd.PersistentFlags().GetStringArray("cfn-ignore")
	if !slices.Equal(stacks, []string{"app", "arn:aws:cloudformation:eu-west-2:111111111111:stack/old/1"}) || !slices.Equal(ignore, []string{"app/Seed"}) {
		t.Errorf("cfn-stack %q, cfn-ignore %q", stacks, ignore)
	}
}

// --cfn-ignore acknowledges resources of the stacks --cfn-stack names, so
// it needs it.
func TestCloudFormationIgnoreNeedsStacks(t *testing.T) {
	_, err := planAWS(context.Background(), importer.Options{CloudFormationIgnore: []string{"Seed"}, AssumeRole: DefaultAssumeRole})
	if err == nil || !strings.Contains(err.Error(), "give --cfn-stack too") {
		t.Errorf("got %v", err)
	}
}

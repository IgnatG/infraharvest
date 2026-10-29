// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package providers

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

func TestAWSAccounts(t *testing.T) {
	ctx := context.Background()
	accounts, err := (&awsScope{accounts: []string{"111122223333", "444455556666"}, assumeRole: DefaultAssumeRole}).accountsToImport(ctx)
	if err != nil || !reflect.DeepEqual(accounts, []string{"111122223333", "444455556666"}) {
		t.Errorf("accounts: %v, %v", accounts, err)
	}
	if accounts, err := (&awsScope{assumeRole: DefaultAssumeRole}).accountsToImport(ctx); err != nil || accounts != nil {
		t.Errorf("one account: %v, %v", accounts, err)
	}
	if err := (&awsScope{accounts: []string{"111122223333"}, organization: true, assumeRole: DefaultAssumeRole}).check(importer.Options{}); err == nil {
		t.Error("both: want an error")
	}
	for name, scope := range map[string]*awsScope{
		"a role for one account":                     {accounts: []string{"111122223333"}, assumeRole: "arn:aws:iam::111122223333:role/x"},
		"a role for one account of the organization": {organization: true, assumeRole: "arn:aws:iam::111122223333:role/x"},
	} {
		if err := scope.check(importer.Options{}); err == nil || !strings.Contains(err.Error(), "{account}") {
			t.Errorf("%s: want an error about {account}, got %v", name, err)
		}
	}
}

// Each of --accounts is a scope of its own, which the importer imports
// apart (see importer.Plan), through the default role in that account,
// each call with a provider of its own.
func TestAWSPlanOfAccounts(t *testing.T) {
	accounts := []string{"111122223333", "444455556666"}
	p, err := (&awsScope{accounts: accounts, assumeRole: DefaultAssumeRole}).plan(context.Background(), importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	plan := p.importerPlan(importer.Options{}, newAWSProvider)
	if plan.Kind != "account" || !slices.Equal(plan.Scopes, accounts) {
		t.Fatalf("kind %q, scopes %v", plan.Kind, plan.Scopes)
	}
	var roles []string
	providers := map[harvest.Provider]bool{}
	for _, account := range plan.Scopes {
		for _, c := range plan.Calls(account) {
			if c.Provider == nil || providers[c.Provider] {
				t.Errorf("%s: a call without a provider of its own", account)
			}
			providers[c.Provider] = true
			roles = append(roles, c.Args[2])
		}
	}
	want := []string{"arn:aws:iam::111122223333:role/infraharvest-readonly", "arn:aws:iam::444455556666:role/infraharvest-readonly"}
	if !slices.Equal(roles, want) {
		t.Errorf("roles %v, want %v", roles, want)
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
	scope, role := &awsScope{profile: "prod"}, "arn:aws:iam::111122223333:role/x"
	options := importer.Options{Layout: byAccount, Services: []string{"vpc", "iam", "wafv2_cloudfront"}}
	check("no regions", scope.calls(options, role), []want{
		{[]string{"", "prod", role}, options.Services, nil, byAccount},
	})

	options.Regions = []string{"eu-west-1", "eu-west-2"}
	check("regions", scope.calls(options, role), []want{
		{[]string{"aws-global", "prod", role}, []string{"iam"}, []string{"aws-global"}, byAccount + "global/"},
		{[]string{"us-east-1", "prod", role}, []string{"wafv2_cloudfront"}, []string{"us-east-1"}, byAccount + "us-east-1/"},
		{[]string{"eu-west-1", "prod", role}, []string{"vpc"}, options.Regions, byAccount + "eu-west-1/"},
		{[]string{"eu-west-2", "prod", role}, []string{"vpc"}, options.Regions, byAccount + "eu-west-2/"},
	})
	// The global and us-east-1-only calls go first, one at a time; the
	// regions' then run at once.
	var first []bool
	for _, c := range scope.calls(options, role) {
		first = append(first, c.first)
	}
	if want := []bool{true, true, false, false}; !slices.Equal(first, want) {
		t.Errorf("first: got %v, want %v", first, want)
	}

	// Global services, and those only in us-east-1, have a root of their
	// own beside a single region's.
	options.Regions = []string{"eu-west-1"}
	check("one region and global services", scope.calls(options, role), []want{
		{[]string{"aws-global", "prod", role}, []string{"iam"}, []string{"aws-global"}, byAccount + "global/"},
		{[]string{"us-east-1", "prod", role}, []string{"wafv2_cloudfront"}, []string{"us-east-1"}, byAccount + "us-east-1/"},
		{[]string{"eu-west-1", "prod", role}, []string{"vpc"}, options.Regions, byAccount + "eu-west-1/"},
	})

	// In us-east-1, those only there are imported with the regional ones.
	options.Regions = []string{"us-east-1", "eu-west-1"}
	check("us-east-1 among the regions", scope.calls(options, role), []want{
		{[]string{"aws-global", "prod", role}, []string{"iam"}, []string{"aws-global"}, byAccount + "global/"},
		{[]string{"us-east-1", "prod", role}, []string{"vpc", "wafv2_cloudfront"}, options.Regions, byAccount + "us-east-1/"},
		{[]string{"eu-west-1", "prod", role}, []string{"vpc"}, options.Regions, byAccount + "eu-west-1/"},
	})

	options.Regions = []string{"eu-west-1"}
	options.Services = []string{"vpc"}
	check("one region, no global services", scope.calls(options, role), []want{
		{[]string{"eu-west-1", "prod", role}, []string{"vpc"}, options.Regions, byAccount},
	})

	options.Services = []string{"vpc", "iam"}
	options.Layout = importer.DefaultLayout
	check("a pattern with {region}", scope.calls(options, role), []want{
		{[]string{"aws-global", "prod", role}, []string{"iam"}, []string{"aws-global"}, importer.DefaultLayout},
		{[]string{"eu-west-1", "prod", role}, []string{"vpc"}, options.Regions, importer.DefaultLayout},
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
		p, err := (&awsScope{assumeRole: role}).plan(ctx, importer.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if p.scopes != nil {
			t.Errorf("%s: scopes %v, want none", role, p.scopes)
		}
		calls := p.calls(importer.Options{}, "")
		if len(calls) != 1 || calls[0].args[2] != want {
			t.Errorf("%s: calls %+v, want one with role %q", role, calls, want)
		}
	}
}

// --cfn-stack reaches the one account the credentials reach, and a stack
// name is looked for in one region: both are refused before any stack is
// read.
func TestCheckAWSStacksRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		scope   *awsScope
		options importer.Options
		want    string
	}{
		"with --accounts":           {&awsScope{stacks: []string{"app"}, accounts: []string{"111122223333"}, assumeRole: DefaultAssumeRole}, importer.Options{}, "without --accounts or --organization"},
		"a name in several regions": {&awsScope{stacks: []string{"app"}, assumeRole: DefaultAssumeRole}, importer.Options{Regions: []string{"eu-west-1", "eu-west-2"}}, "--cfn-stack app: a stack name is looked for in one region"},
	} {
		if err := c.scope.check(c.options); err == nil || !strings.Contains(err.Error(), c.want) {
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
	f, err := config.Load(path, Profiles())
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
	err := (&awsScope{ignore: []string{"Seed"}, assumeRole: DefaultAssumeRole}).check(importer.Options{})
	if err == nil || !strings.Contains(err.Error(), "give --cfn-stack too") {
		t.Errorf("got %v", err)
	}
}

// A --catalogue that can't be read stops a --cfn-stack plan before any
// stack is read.
func TestPlanAWSStacksBadCatalogue(t *testing.T) {
	_, err := (&awsScope{stacks: []string{"app"}, assumeRole: DefaultAssumeRole}).plan(context.Background(), importer.Options{
		Catalogue: filepath.Join(t.TempDir(), "missing.yaml"),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "--catalogue: ") {
		t.Errorf("got %v, want a --catalogue error", err)
	}
}

// The AWS command's flags go into the scope the importer is handed, which
// says what the importer asks of it: whether --cfn-stack scopes the
// import, why --resource-explorer lists in part, and --profile on the
// state in S3 that --managed-state names.
func TestAWSScope(t *testing.T) {
	flags := pflag.NewFlagSet("aws", pflag.ContinueOnError)
	options := importer.Options{}
	awsFlags(flags, &options)
	if s := options.ProviderScope.(harvest.StackScoped); s.StackScoped() {
		t.Error("scoped to stacks without --cfn-stack")
	}
	if why := options.ProviderScope.(harvest.PartialListingScope).ListsInPart(); why != "" {
		t.Errorf("lists in part without --resource-explorer: %q", why)
	}
	if err := flags.Parse([]string{"--cfn-stack=app", "--resource-explorer", "--profile=prod"}); err != nil {
		t.Fatal(err)
	}
	if !options.ProviderScope.(harvest.StackScoped).StackScoped() {
		t.Error("--cfn-stack: not scoped to stacks")
	}
	if why := options.ProviderScope.(harvest.PartialListingScope).ListsInPart(); !strings.Contains(why, "--resource-explorer") {
		t.Errorf("--resource-explorer: lists in part because %q", why)
	}
	if got := options.ProviderScope.(harvest.ManagedStateScope).ManagedStateSource("s3://state/roots/"); got != "s3://state/roots/?profile=prod" {
		t.Errorf("--profile: managed state read from %q", got)
	}
}

// --profile reads the state in S3 --managed-state names, unless the source
// names a profile of its own; other sources stay as they are.
func TestAWSManagedStateSource(t *testing.T) {
	for _, tc := range []struct{ source, profile, want string }{
		{"s3://state/roots/?region=eu-west-2", "prod", "s3://state/roots/?profile=prod&region=eu-west-2"},
		{"s3://state/roots/?profile=state", "prod", "s3://state/roots/?profile=state"},
		{"s3://state/roots/", "default", "s3://state/roots/"},
		{"s3://state/roots/", "", "s3://state/roots/"},
		{"gs://state/roots/", "prod", "gs://state/roots/"},
		{"terraform.tfstate", "prod", "terraform.tfstate"},
	} {
		if got := (&awsScope{profile: tc.profile}).ManagedStateSource(tc.source); got != tc.want {
			t.Errorf("ManagedStateSource(%q) with profile %q = %q, want %q", tc.source, tc.profile, got, tc.want)
		}
	}
}

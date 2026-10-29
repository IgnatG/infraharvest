// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package providers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/managed"
	s3store "github.com/IgnatG/infraharvest/internal/managed/stores/s3"
	awsprovider "github.com/IgnatG/infraharvest/internal/providers/aws"
)

// DefaultAssumeRole is the role --accounts and --organization assume in
// each account, where {account} stands for the account's ID.
const DefaultAssumeRole = "arn:aws:iam::{account}:role/infraharvest-readonly"

func init() {
	define(definition{
		name:        "aws",
		newProvider: newAWSProvider,
		stores:      func(s *managed.Stores) { s.S3 = s3store.New },
		short:       "Generate Terraform configuration for the resources of AWS accounts",
		long: "List the resources of the services --services names in an AWS account, and\n" +
			"generate their Terraform configuration with import blocks. The credentials come\n" +
			"from --profile, or the environment. Without --regions, the import covers the\n" +
			"profile's region; with it, each region, and global services, such as\n" +
			"iam and route53, once on their own.\n\n" +
			"--accounts or --organization imports several accounts in one run, each through\n" +
			"the role --assume-role names. Nothing is applied and no state is written.",
		sampleServices: "vpc,s3",
		sampleIDs:      "vpc=vpc-0a1b2c3d",
		parallelScopes: "accounts of --accounts or --organization and regions of --regions",
		flags:          awsFlags,
	})
}

// newAWSProvider returns a new AWS provider.
func newAWSProvider() harvest.Provider {
	return &awsprovider.Provider{}
}

// awsScope is what the AWS command's own flags say of where to list and
// what to import, besides --regions: the command's harvest scope (see
// importer.Options.ProviderScope), which the command reads, and hands the
// provider through each call's args.
type awsScope struct {
	// profile is the AWS profile; the managed state in S3 is read with it
	// too.
	profile string
	// accounts are the accounts to import, each through assumeRole.
	accounts []string
	// organization imports every active account of the organization.
	organization bool
	// assumeRole is the role to assume, where {account} stands for each
	// account's ID.
	assumeRole string
	// resourceExplorer skips the services Resource Explorer shows are
	// empty in a region.
	resourceExplorer bool
	// stacks are the CloudFormation stacks, by name or stack ID, whose
	// resources, and their nested stacks', are all the import takes.
	stacks []string
	// ignore are the resources of those stacks, by logical ID or as
	// <stack>/<logical ID>, that Terraform can't import, such as custom
	// resources, which the stacks may be deleted without.
	ignore []string
}

// StackScoped reports whether --cfn-stack scopes the import to
// CloudFormation stacks (see harvest.StackScoped).
func (s *awsScope) StackScoped() bool {
	return len(s.stacks) > 0
}

// ListsInPart says --resource-explorer leaves services out of the listing
// (see harvest.PartialListingScope).
func (s *awsScope) ListsInPart() string {
	if s.resourceExplorer {
		return "--resource-explorer leaves out the services its index shows nothing of"
	}
	return ""
}

// ManagedStateSource adds --profile to an s3:// source that names none,
// which would otherwise read with the default credential chain: the
// profile the import was told to use. Other sources stay as they are (see
// harvest.ManagedStateScope).
func (s *awsScope) ManagedStateSource(source string) string {
	if !strings.HasPrefix(source, "s3://") || s.profile == "" || s.profile == "default" {
		return source
	}
	u, err := url.Parse(source)
	if err != nil {
		return source // managed.Load reports it.
	}
	query := u.Query()
	if query.Get("profile") != "" {
		return source
	}
	query.Set("profile", s.profile)
	u.RawQuery = query.Encode()
	return u.String()
}

// awsFlags adds the AWS command's own flags: --regions in options, the
// rest in the command's scope, which options carries.
func awsFlags(flags *pflag.FlagSet, options *importer.Options) (checker, planner) {
	s := &awsScope{}
	options.ProviderScope = s
	flags.StringVar(&s.profile, "profile", "default", "AWS profile, from the shared configuration and credentials files, whose credentials the import uses")
	flags.StringSliceVar(&options.Regions, "regions", nil, "regions to import, each into roots of its own, such as eu-west-1,us-east-1 (default: the region of the profile or the environment)")
	flags.StringSliceVar(&s.accounts, "accounts", nil, "accounts to import, each through the role --assume-role names")
	flags.BoolVar(&s.organization, "organization", false, "import every active account of the organization (needs organizations:ListAccounts), each through the role --assume-role names")
	flags.StringVar(&s.assumeRole, "assume-role", DefaultAssumeRole, "role to assume in each account of --accounts or --organization; {account} stands for the account ID. Without them, a role ARN assumes that role")
	flags.BoolVar(&s.resourceExplorer, "resource-explorer", false, "don't list the services that AWS Resource Explorer's index shows have nothing in a region (needs an index and a default view; the index can miss resources created in the last hours, see "+DocsURL+"aws/#resource-explorer)")
	flags.StringArrayVar(&s.stacks, "cfn-stack", nil, "import the resources of this CloudFormation stack, and of the stacks nested in it, and nothing else, by name or stack ID (ARN; a deleted stack's for 90 days); repeat for other stacks. The report says whether each stack can be deleted once its resources are in Terraform, and writes a copy of its template with Retain added (see "+DocsURL+"aws/#migrating-a-cloudformation-stack)")
	flags.StringArrayVar(&s.ignore, "cfn-ignore", nil, "with --cfn-stack: acknowledge that the stacks may be deleted without this resource, which Terraform can't import, such as a custom resource, by its logical ID, or as <stack>/<logical ID> where several stacks have it; repeat for others. It still needs Retain, as the copy of the template with Retain added gives it, so that deleting the stack never calls its handler. The CDK's Custom::S3AutoDeleteObjects and the like, which destroy what the stack's resources hold, can't be acknowledged (see "+DocsURL+"aws/#custom-resources)")
	return s.check, s.plan
}

// check rejects --cfn-ignore without --cfn-stack, --cfn-stack with
// several accounts or with a stack name in several regions, --accounts
// with --organization, and several accounts through a role without
// {account}, or into a layout without it.
func (s *awsScope) check(options importer.Options) error {
	several := s.organization || len(s.accounts) > 0
	if len(s.ignore) > 0 && len(s.stacks) == 0 {
		return errors.New("--cfn-ignore acknowledges resources of the stacks --cfn-stack names: give --cfn-stack too")
	}
	if len(s.stacks) > 0 {
		if several {
			return errors.New("--cfn-stack imports stacks of the account the credentials reach: use it without --accounts or --organization, with --assume-role naming a role ARN to reach another account")
		}
		if len(options.Regions) > 1 {
			for _, ref := range s.stacks {
				if !strings.HasPrefix(ref, "arn:") {
					return fmt.Errorf("--cfn-stack %s: a stack name is looked for in one region: give --regions one region, or the stack's ID (its ARN)", ref)
				}
			}
		}
		return nil
	}
	if s.organization && len(s.accounts) > 0 {
		return errors.New("use either --accounts or --organization")
	}
	if !several {
		return nil
	}
	if !strings.Contains(s.assumeRole, "{account}") {
		return fmt.Errorf("--assume-role %q needs {account} to reach several accounts", s.assumeRole)
	}
	return checkScopesApart(options.Layout, "account")
}

// plan imports the account the credentials reach or, with --accounts or
// --organization, each of several accounts through the role --assume-role
// names.
func (s *awsScope) plan(ctx context.Context, options importer.Options) (plan, error) {
	if len(s.stacks) > 0 {
		return s.planStacks(ctx, options)
	}
	accounts, err := s.accountsToImport(ctx)
	if err != nil {
		return plan{}, err
	}
	if accounts == nil {
		// A role without {account} is a role ARN, assumed as it is.
		roleARN := ""
		if !strings.Contains(s.assumeRole, "{account}") {
			roleARN = s.assumeRole
		}
		return plan{calls: func(accountOptions importer.Options, _ string) []call {
			return s.calls(accountOptions, roleARN)
		}, newProvider: s.newProvider}, nil
	}
	return plan{kind: "account", scopes: accounts, calls: func(accountOptions importer.Options, account string) []call {
		log.Printf("aws: importing account %s", account)
		return s.calls(accountOptions, strings.ReplaceAll(s.assumeRole, "{account}", account))
	}, newProvider: s.newProvider}, nil
}

// planStacks imports the resources of the CloudFormation stacks
// --cfn-stack names, and of those nested in them, read first (see
// awsprovider.ResolveStacks): in the regions the stacks are in, with the
// global services in their own call, as --regions would; and, without
// --services, only the services that list the types of the stacks'
// resources. A stack named by name is looked for in the one region of
// --regions, or the profile's.
func (s *awsScope) planStacks(ctx context.Context, options importer.Options) (plan, error) {
	region := awsprovider.ProfileRegion
	if len(options.Regions) == 1 {
		region = options.Regions[0]
	}
	roleARN := ""
	if !strings.Contains(s.assumeRole, "{account}") {
		roleARN = s.assumeRole
	}
	cat, err := catalogue.Load("aws", options.Catalogue)
	if err != nil {
		return plan{}, fmt.Errorf("--catalogue: %w", err)
	}
	scope, err := awsprovider.ResolveStacks(ctx, s.profile, roleARN, region, s.stacks, cat)
	if err != nil {
		return plan{}, err
	}
	if err := scope.Acknowledge(s.ignore); err != nil {
		return plan{}, err
	}
	for _, line := range scope.Summary() {
		log.Printf("aws: %s", line)
	}
	options.Regions = scope.Regions()
	if len(options.Services) == 0 {
		options.Services = scope.Services(cat)
		log.Printf("aws: listing the services of the stacks' resources: %s", strings.Join(options.Services, ", "))
	}
	return plan{
		calls: func(callOptions importer.Options, _ string) []call {
			callOptions.Regions, callOptions.Services = options.Regions, options.Services
			if len(callOptions.Services) == 0 {
				// Nothing to list, as where every resource is a custom
				// resource: one call, in the stacks' first region, still
				// reports the stacks, with what keeps each from being safe
				// to delete.
				log.Printf("aws: no service lists a resource of the stacks: importing nothing, to report on the stacks")
				callOptions.Regions = callOptions.Regions[:1]
				return []call{s.call(callOptions, roleARN, callOptions.Regions[0], "region "+callOptions.Regions[0])}
			}
			return s.calls(callOptions, roleARN)
		},
		newProvider: func() harvest.Provider {
			p := s.newProvider().(*awsprovider.Provider)
			p.SetStackScope(scope)
			return p
		},
	}, nil
}

// newProvider returns the provider of each Import call: an AWS provider
// that uses Resource Explorer if --resource-explorer says so, which each
// run's own providers carry, not the process.
func (s *awsScope) newProvider() harvest.Provider {
	p := &awsprovider.Provider{}
	p.UseResourceExplorer(s.resourceExplorer)
	return p
}

// accountsToImport returns the accounts --accounts or --organization
// names, or nil to import the one account the credentials reach.
func (s *awsScope) accountsToImport(ctx context.Context) ([]string, error) {
	if !s.organization && len(s.accounts) == 0 {
		return nil, nil
	}
	if !s.organization {
		// Even a single account goes through the role, as --accounts asks.
		return s.accounts, nil
	}
	accounts, err := awsprovider.OrganizationAccounts(ctx, s.profile)
	if err != nil {
		return nil, fmt.Errorf("list the organization's accounts: %w", err)
	}
	if len(accounts) == 0 {
		return nil, errors.New("the organization has no active accounts")
	}
	return accounts, nil
}

// calls returns the Import calls that cover one account, through the role
// roleARN names, none for "". Without
// --regions, one call lists in the default region. With it, global
// services are imported once, in the global region; those that only work
// in us-east-1 there, with the regional ones when us-east-1 is one of
// --regions; and the rest in each of --regions, in order. The global and
// us-east-1-only calls go first, one at a time, and the regions' then
// --parallel at once (see importer.Call).
func (s *awsScope) calls(options importer.Options, roleARN string) []call {
	if len(options.Regions) == 0 {
		return []call{s.call(options, roleARN, awsprovider.ProfileRegion, "the default region")}
	}

	global, eastOnly, regional := groupAWSServices(options)
	// The services that only work in us-east-1 join that region's call
	// when there is one, so that the two don't share a root.
	eastWithRegional := len(eastOnly) > 0 && len(regional) > 0 && slices.Contains(options.Regions, awsprovider.GlobalHomeRegion)
	// Each call needs a directory of its own when there are several, as
	// the default layout's {region} gives them: global/, us-east-1/ and
	// each region's, unless the pattern already has one.
	calls := 0
	if len(global) > 0 {
		calls++
	}
	if len(eastOnly) > 0 && !eastWithRegional {
		calls++
	}
	if len(regional) > 0 {
		calls += len(options.Regions)
	}
	separate := calls > 1 && !strings.Contains(options.Layout, "{region}")
	layout := func(dir string) string {
		if separate {
			return options.Layout + dir + "/"
		}
		return options.Layout
	}

	var result []call
	if len(global) > 0 {
		globalOptions := options
		globalOptions.Services = global
		globalOptions.Regions = []string{awsprovider.GlobalPseudoRegion}
		globalOptions.Layout = layout("global")
		globalCall := s.call(globalOptions, roleARN, awsprovider.GlobalPseudoRegion, "the default region")
		globalCall.first = true
		result = append(result, globalCall)
	}
	if len(eastOnly) > 0 && !eastWithRegional {
		eastOptions := options
		eastOptions.Services = eastOnly
		eastOptions.Regions = []string{awsprovider.GlobalHomeRegion}
		eastOptions.Layout = layout(awsprovider.GlobalHomeRegion)
		eastCall := s.call(eastOptions, roleARN, awsprovider.GlobalHomeRegion, "region "+awsprovider.GlobalHomeRegion)
		eastCall.first = true
		result = append(result, eastCall)
	}
	if len(regional) == 0 {
		return result
	}
	for _, region := range options.Regions {
		regionOptions := options
		regionOptions.Services = regional
		if eastWithRegional && region == awsprovider.GlobalHomeRegion {
			regionOptions.Services = append(slices.Clone(regional), eastOnly...)
		}
		regionOptions.Layout = layout(region)
		result = append(result, s.call(regionOptions, roleARN, region, "region "+region))
	}
	return result
}

// call returns the call that imports options in region, through the role
// roleARN names, none for "". The AWS provider reads the region, the
// profile and the role to assume.
func (s *awsScope) call(options importer.Options, roleARN, region, where string) call {
	return call{options: options, args: []string{region, s.profile, roleARN}, where: where}
}

// groupAWSServices resolves --services and --skip, and splits the
// services, each in their order, into global ones, those that only work in
// us-east-1, and regional ones.
func groupAWSServices(options importer.Options) (global, eastOnly, regional []string) {
	for _, service := range importer.ResolveServices(newAWSProvider(), options) {
		switch {
		case slices.Contains(awsprovider.GlobalServices, service):
			global = append(global, service)
		case slices.Contains(awsprovider.EastOnlyServices, service):
			eastOnly = append(eastOnly, service)
		default:
			regional = append(regional, service)
		}
	}
	return global, eastOnly, regional
}

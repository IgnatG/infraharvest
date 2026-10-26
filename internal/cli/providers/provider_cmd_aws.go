// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package providers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
	awsprovider "github.com/IgnatG/infraharvest/internal/providers/aws"
)

// DefaultAssumeRole is the role --accounts and --organization assume in
// each account, where {account} stands for the account's ID.
const DefaultAssumeRole = "arn:aws:iam::{account}:role/infraharvest-readonly"

func init() {
	define(definition{
		name:        "aws",
		newProvider: newAWSProvider,
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

// awsFlags adds the AWS command's own flags. They all live in options.
func awsFlags(flags *pflag.FlagSet, options *importer.Options) planner {
	flags.StringVar(&options.Profile, "profile", "default", "AWS profile, from the shared configuration and credentials files, whose credentials the import uses")
	flags.StringSliceVar(&options.Regions, "regions", nil, "regions to import, each into roots of its own, such as eu-west-1,us-east-1 (default: the region of the profile or the environment)")
	flags.StringSliceVar(&options.Accounts, "accounts", nil, "accounts to import, each through the role --assume-role names")
	flags.BoolVar(&options.Organization, "organization", false, "import every active account of the organization (needs organizations:ListAccounts), each through the role --assume-role names")
	flags.StringVar(&options.AssumeRole, "assume-role", DefaultAssumeRole, "role to assume in each account of --accounts or --organization; {account} stands for the account ID. Without them, a role ARN assumes that role")
	flags.BoolVar(&options.ResourceExplorer, "resource-explorer", false, "don't list the services that AWS Resource Explorer's index shows have nothing in a region (needs an index and a default view; the index can miss resources created in the last hours, see "+DocsURL+"aws/#resource-explorer)")
	flags.StringArrayVar(&options.CloudFormationStacks, "cfn-stack", nil, "import the resources of this CloudFormation stack, and of the stacks nested in it, and nothing else, by name or stack ID (ARN; a deleted stack's for 90 days); repeat for other stacks. The report says whether each stack can be deleted once its resources are in Terraform, and writes a copy of its template with Retain added (see "+DocsURL+"aws/#migrating-a-cloudformation-stack)")
	flags.StringArrayVar(&options.CloudFormationIgnore, "cfn-ignore", nil, "with --cfn-stack: acknowledge that the stacks may be deleted without this resource, which Terraform can't import, such as a custom resource, by its logical ID, or as <stack>/<logical ID> where several stacks have it; repeat for others. The CDK's Custom::S3AutoDeleteObjects and the like, which destroy what the stack's resources hold, can't be acknowledged (see "+DocsURL+"aws/#custom-resources)")
	return planAWS
}

// planAWS imports the account the credentials reach or, with --accounts or
// --organization, each of several accounts through the role --assume-role
// names.
func planAWS(ctx context.Context, options importer.Options) (plan, error) {
	awsprovider.UseResourceExplorer(options.ResourceExplorer)
	accounts, err := awsAccounts(ctx, options)
	if err != nil {
		return plan{}, err
	}
	if len(options.CloudFormationIgnore) > 0 && len(options.CloudFormationStacks) == 0 {
		return plan{}, errors.New("--cfn-ignore acknowledges resources of the stacks --cfn-stack names: give --cfn-stack too")
	}
	if len(options.CloudFormationStacks) > 0 {
		if accounts != nil {
			return plan{}, errors.New("--cfn-stack imports stacks of the account the credentials reach: use it without --accounts or --organization, with --assume-role naming a role ARN to reach another account")
		}
		return planAWSStacks(ctx, options)
	}
	if accounts == nil {
		return plan{calls: func(accountOptions importer.Options, _ string) []call {
			// A role without {account} is a role ARN, assumed as it is.
			if !strings.Contains(accountOptions.AssumeRole, "{account}") {
				accountOptions.RoleARN = accountOptions.AssumeRole
			}
			return awsCalls(accountOptions)
		}}, nil
	}
	return plan{kind: "account", scopes: accounts, calls: func(accountOptions importer.Options, account string) []call {
		log.Printf("aws: importing account %s", account)
		accountOptions.RoleARN = strings.ReplaceAll(accountOptions.AssumeRole, "{account}", account)
		return awsCalls(accountOptions)
	}}, nil
}

// planAWSStacks imports the resources of the CloudFormation stacks
// --cfn-stack names, and of those nested in them, read first (see
// awsprovider.ResolveStacks): in the regions the stacks are in, with the
// global services in their own call, as --regions would; and, without
// --services, only the services that list the types of the stacks'
// resources. A stack named by name is looked for in the one region of
// --regions, or the profile's.
func planAWSStacks(ctx context.Context, options importer.Options) (plan, error) {
	region := awsprovider.ProfileRegion
	switch len(options.Regions) {
	case 0:
	case 1:
		region = options.Regions[0]
	default:
		for _, ref := range options.CloudFormationStacks {
			if !strings.HasPrefix(ref, "arn:") {
				return plan{}, fmt.Errorf("--cfn-stack %s: a stack name is looked for in one region: give --regions one region, or the stack's ID (its ARN)", ref)
			}
		}
	}
	roleARN := ""
	if !strings.Contains(options.AssumeRole, "{account}") {
		roleARN = options.AssumeRole
	}
	cat, err := catalogue.Load("aws", options.Catalogue)
	if err != nil {
		return plan{}, fmt.Errorf("--catalogue: %w", err)
	}
	scope, err := awsprovider.ResolveStacks(ctx, options.Profile, roleARN, region, options.CloudFormationStacks, cat)
	if err != nil {
		return plan{}, err
	}
	if err := scope.Acknowledge(options.CloudFormationIgnore); err != nil {
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
			callOptions.Regions, callOptions.Services, callOptions.RoleARN = options.Regions, options.Services, roleARN
			return awsCalls(callOptions)
		},
		newProvider: func() harvest.Provider {
			p := &awsprovider.Provider{}
			p.SetStackScope(scope)
			return p
		},
	}, nil
}

// awsAccounts returns the accounts --accounts or --organization names, or
// nil to import the one account the credentials reach.
func awsAccounts(ctx context.Context, options importer.Options) ([]string, error) {
	switch {
	case options.Organization && len(options.Accounts) > 0:
		return nil, errors.New("use either --accounts or --organization")
	case !options.Organization && len(options.Accounts) == 0:
		return nil, nil
	}
	if !strings.Contains(options.AssumeRole, "{account}") {
		return nil, fmt.Errorf("--assume-role %q needs {account} to reach several accounts", options.AssumeRole)
	}
	if err := checkScopesApart(options.Layout, "account"); err != nil {
		return nil, err
	}
	if !options.Organization {
		// Even a single account goes through the role, as --accounts asks.
		return options.Accounts, nil
	}
	accounts, err := awsprovider.OrganizationAccounts(ctx, options.Profile)
	if err != nil {
		return nil, fmt.Errorf("list the organization's accounts: %w", err)
	}
	if len(accounts) == 0 {
		return nil, errors.New("the organization has no active accounts")
	}
	return accounts, nil
}

// awsCalls returns the Import calls that cover one account. Without
// --regions, one call lists in the default region. With it, global
// services are imported once, in the global region; those that only work
// in us-east-1 there, with the regional ones when us-east-1 is one of
// --regions; and the rest in each of --regions, in order. The global and
// us-east-1-only calls go first, one at a time, and the regions' then
// --parallel at once (see importer.ImportCalls).
func awsCalls(options importer.Options) []call {
	if len(options.Regions) == 0 {
		return []call{awsCall(options, awsprovider.ProfileRegion, "the default region")}
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
		globalCall := awsCall(globalOptions, awsprovider.GlobalPseudoRegion, "the default region")
		globalCall.first = true
		result = append(result, globalCall)
	}
	if len(eastOnly) > 0 && !eastWithRegional {
		eastOptions := options
		eastOptions.Services = eastOnly
		eastOptions.Regions = []string{awsprovider.GlobalHomeRegion}
		eastOptions.Layout = layout(awsprovider.GlobalHomeRegion)
		eastCall := awsCall(eastOptions, awsprovider.GlobalHomeRegion, "region "+awsprovider.GlobalHomeRegion)
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
		result = append(result, awsCall(regionOptions, region, "region "+region))
	}
	return result
}

// awsCall returns the call that imports options in region. The AWS
// provider reads the region, the profile and the role to assume.
func awsCall(options importer.Options, region, where string) call {
	return call{options: options, args: []string{region, options.Profile, options.RoleARN}, where: where}
}

// groupAWSServices resolves --services and --skip, and splits the
// services, each in their order, into global ones, those that only work in
// us-east-1, and regional ones.
func groupAWSServices(options importer.Options) (global, eastOnly, regional []string) {
	for _, service := range importer.ResolveServices(newAWSProvider(), options).Services {
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

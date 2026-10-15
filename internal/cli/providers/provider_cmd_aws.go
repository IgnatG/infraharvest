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
			"profile's region; with it, each region in turn, and global services, such as\n" +
			"iam and route53, once on their own.\n\n" +
			"--accounts or --organization imports several accounts in one run, each through\n" +
			"the role --assume-role names. Nothing is applied and no state is written.",
		sampleServices: "vpc,s3",
		sampleIDs:      "vpc=vpc-0a1b2c3d",
		parallelScopes: "accounts of --accounts or --organization",
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
	flags.StringSliceVar(&options.Regions, "regions", nil, "regions to import, each in turn, such as eu-west-1,us-east-1 (default: the region of the profile or the environment)")
	flags.StringSliceVar(&options.Accounts, "accounts", nil, "accounts to import, each through the role --assume-role names")
	flags.BoolVar(&options.Organization, "organization", false, "import every active account of the organization (needs organizations:ListAccounts), each through the role --assume-role names")
	flags.StringVar(&options.AssumeRole, "assume-role", DefaultAssumeRole, "role to assume in each account of --accounts or --organization; {account} stands for the account ID. Without them, a role ARN assumes that role")
	flags.BoolVar(&options.ResourceExplorer, "resource-explorer", false, "don't list the services that AWS Resource Explorer's index shows have nothing in a region (needs an index and a default view; the index can miss resources created in the last hours, see "+DocsURL+"aws/#resource-explorer)")
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
// in us-east-1 there; and the rest in each of --regions, in order.
func awsCalls(options importer.Options) []call {
	if len(options.Regions) == 0 {
		return []call{awsCall(options, awsprovider.ProfileRegion, "the default region")}
	}

	global, eastOnly, regional := groupAWSServices(options)
	var calls []call
	if len(global) > 0 {
		globalOptions := options
		globalOptions.Services = global
		globalOptions.Regions = []string{awsprovider.GlobalPseudoRegion}
		calls = append(calls, awsCall(globalOptions, awsprovider.GlobalPseudoRegion, "the default region"))
	}
	if len(eastOnly) > 0 {
		eastOptions := options
		eastOptions.Services = eastOnly
		eastOptions.Regions = []string{awsprovider.GlobalHomeRegion}
		calls = append(calls, awsCall(eastOptions, awsprovider.GlobalHomeRegion, "region "+awsprovider.GlobalHomeRegion))
	}
	if len(regional) == 0 {
		return calls
	}
	// Regional roots need a directory of their own per region when there
	// are several regions, or global roots beside them, unless the
	// pattern already has one.
	perRegion := (len(options.Regions) > 1 || len(global) > 0) && !strings.Contains(options.Layout, "{region}")
	for _, region := range options.Regions {
		regionOptions := options
		regionOptions.Services = regional
		if perRegion {
			regionOptions.Layout += region + "/"
		}
		calls = append(calls, awsCall(regionOptions, region, "region "+region))
	}
	return calls
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

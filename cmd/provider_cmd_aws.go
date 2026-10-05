// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !minimal || aws

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	awsterraformer "github.com/IgnatG/infraharvest/providers/aws"
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/spf13/cobra"
)

func newCmdAwsImporter(options ImportOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aws",
		Short: "Import current state to Terraform configuration from AWS",
		Long:  "Import current state to Terraform configuration from AWS",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			accounts, err := awsAccounts(ctx, options)
			if err != nil {
				return err
			}
			if len(accounts) == 0 {
				// One account: a full role ARN is assumed as it is.
				if !strings.Contains(options.AssumeRole, "{account}") {
					options.RoleARN = options.AssumeRole
				}
				return importAWSAccount(options)
			}
			return importAWSAccounts(options, accounts, importAWSAccount)
		},
	}
	cmd.AddCommand(listCmd(newAWSProvider()))
	baseProviderFlags(cmd.PersistentFlags(), &options, "vpc,subnet,nacl", "elb=id1:id2:id4")

	cmd.PersistentFlags().StringVarP(&options.Profile, "profile", "", "default", "prod")
	cmd.PersistentFlags().StringSliceVarP(&options.Regions, "regions", "", []string{}, "eu-west-1,eu-west-2,us-east-1")
	cmd.PersistentFlags().StringSliceVar(&options.Accounts, "accounts", nil, "accounts to import, each through the role --assume-role names")
	cmd.PersistentFlags().BoolVar(&options.Organization, "organization", false, "import every active account of the organization (needs organizations:ListAccounts), each through the role --assume-role names")
	cmd.PersistentFlags().StringVar(&options.AssumeRole, "assume-role", DefaultAssumeRole, "role to assume in each account of --accounts or --organization; {account} stands for the account ID. Without them, a role ARN assumes that role")
	return cmd
}

// importAWSAccounts imports accounts one by one, each through the role
// --assume-role names with {account} filled in (see awsAccounts), with
// importAccount. An account that can't be imported, such as one whose role
// can't be assumed, is recorded as a failure of the run and the others go
// on; an interrupt stops the run.
func importAWSAccounts(options ImportOptions, accounts []string, importAccount func(ImportOptions) error) error {
	for _, account := range accounts {
		log.Printf("aws: importing account %s", account)
		accountOptions := options
		accountOptions.RoleARN = strings.ReplaceAll(options.AssumeRole, "{account}", account)
		err := importAccount(accountOptions)
		if err == nil {
			continue
		}
		if activeRun == nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Printf("aws: account %s: %v", account, err)
		activeRun.recordFailure(options, fmt.Errorf("account %s: %w", account, err))
	}
	return nil
}

// importAWSAccount imports one account: global, us-east-1-only and regional
// resources, through options.RoleARN if set.
func importAWSAccount(options ImportOptions) error {
	originalRegions := options.Regions
	originalPathPattern := options.PathPattern
	if len(options.Regions) == 0 {
		return importRegionResources(options, options.PathPattern, awsterraformer.NoRegion, false)
	}

	shouldSpecifyPathRegion := len(options.Regions) > 1
	globalResources, eastOnlyResources, regionalResources := groupAWSResources(options)
	options.Resources = globalResources
	options.Regions = []string{awsterraformer.GlobalRegion}
	if err := importGlobalResources(options); err != nil {
		return err
	}

	options.Resources = eastOnlyResources
	options.Regions = []string{awsterraformer.MainRegionPublicPartition}
	if err := importEastOnlyResources(options); err != nil {
		return err
	}

	options.Resources = regionalResources
	options.Regions = originalRegions
	if len(options.Resources) == 0 { // don't import anything and potentially override global resources
		return nil
	}
	if len(globalResources) > 0 {
		shouldSpecifyPathRegion = true // we should keep global resources away from regional
	}
	for _, region := range originalRegions {
		if err := importRegionResources(options, originalPathPattern, region, shouldSpecifyPathRegion); err != nil {
			return err
		}
	}
	return nil
}

// DefaultAssumeRole is the role --accounts and --organization assume in
// each account: the read-only role permissions/aws creates.
const DefaultAssumeRole = "arn:aws:iam::{account}:role/infraharvest-readonly"

// awsAccounts returns the accounts to import one by one: --accounts, or
// the organization's with --organization; none for a single account.
func awsAccounts(ctx context.Context, options ImportOptions) ([]string, error) {
	switch {
	case options.Organization && len(options.Accounts) > 0:
		return nil, errors.New("use either --accounts or --organization")
	case !options.Organization && len(options.Accounts) == 0:
		return nil, nil
	}
	// Several accounts, one role each: a role ARN of one account would
	// import that account every time.
	if !strings.Contains(options.AssumeRole, "{account}") {
		return nil, fmt.Errorf("--assume-role %q needs {account} to reach several accounts", options.AssumeRole)
	}
	if options.Organization {
		accounts, err := awsterraformer.OrganizationAccounts(ctx, options.Profile)
		if err != nil {
			return nil, fmt.Errorf("list the organization's accounts: %w", err)
		}
		if len(accounts) == 0 {
			return nil, errors.New("the organization has no active accounts")
		}
		return accounts, nil
	}
	return options.Accounts, nil
}

// groupAWSResources returns the global, us-east-1-only and regional
// services of options.Resources. "*" is expanded first (see
// resolveServices), so that the global services it stands for are
// imported once, not once per region.
func groupAWSResources(options ImportOptions) (global, eastOnly, regional []string) {
	return parseAndGroupResources(resolveServices(newAWSProvider(), options).Resources)
}

// returns global, east-only, regional resources
func parseAndGroupResources(allResources []string) ([]string, []string, []string) {
	var globalResources, eastOnlyResources, regionalResources []string
	for _, resourceName := range allResources {
		switch {
		case contains(awsterraformer.SupportedGlobalResources, resourceName):
			globalResources = append(globalResources, resourceName)
		case contains(awsterraformer.SupportedEastOnlyResources, resourceName):
			eastOnlyResources = append(eastOnlyResources, resourceName)
		default:
			regionalResources = append(regionalResources, resourceName)
		}
	}
	return globalResources, eastOnlyResources, regionalResources
}

func importGlobalResources(options ImportOptions) error {
	if len(options.Resources) > 0 {
		return importRegionResources(options, options.PathPattern, awsterraformer.GlobalRegion, false)
	}
	return nil
}

func importEastOnlyResources(options ImportOptions) error {
	if len(options.Resources) > 0 {
		return importRegionResources(options, options.PathPattern, awsterraformer.MainRegionPublicPartition, false)
	}
	return nil
}

func importRegionResources(options ImportOptions, originalPathPattern string, region string, shouldSpecifyPathRegion bool) error {
	provider := newAWSProvider()
	options.PathPattern = originalPathPattern
	if region != awsterraformer.GlobalRegion && region != awsterraformer.NoRegion {
		if shouldSpecifyPathRegion && !strings.Contains(options.PathPattern, "{region}") {
			options.PathPattern += region + "/"
		}
		log.Println(provider.GetName() + " importing region " + region)
	} else {
		log.Println(provider.GetName() + " importing default region")
	}
	err := Import(provider, options, []string{region, options.Profile, options.RoleARN})
	if err != nil {
		return err
	}
	return nil
}

func newAWSProvider() terraformutils.ProviderGenerator {
	return &awsterraformer.AWSProvider{}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func init() {
	registerProvider("aws", newCmdAwsImporter, newAWSProvider)
}

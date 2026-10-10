// Copyright 2019 The Terraformer Authors.
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

//go:build !minimal || azure

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	azure_terraforming "github.com/IgnatG/infraharvest/providers/azure"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/spf13/cobra"
)

func newCmdAzureImporter(options ImportOptions) *cobra.Command {
	var subscriptions []string
	managementGroup := ""
	cmd := &cobra.Command{
		Use:   "azure",
		Short: "Import current state to Terraform configuration from Azure",
		Long:  "Import current state to Terraform configuration from Azure",
		RunE: func(c *cobra.Command, _ []string) error {
			if managementGroup != "" {
				ctx := context.Background()
				if c != nil && c.Context() != nil {
					ctx = c.Context()
				}
				found, err := azure_terraforming.Subscriptions(ctx, managementGroup)
				if err != nil {
					return err
				}
				if len(found) == 0 {
					return errors.New("no enabled subscriptions under --management-group " + managementGroup)
				}
				log.Printf("azurerm: %d subscriptions under management group %s", len(found), managementGroup)
				for _, s := range found {
					if !slices.Contains(subscriptions, s) {
						subscriptions = append(subscriptions, s)
					}
				}
			}
			if len(subscriptions) == 0 {
				// The subscription of ARM_SUBSCRIPTION_ID.
				return Import(newAzureProvider(), options, []string{options.ResourceGroup})
			}
			// Each subscription gets roots of its own: without {account},
			// they would write over each other's.
			if len(subscriptions) > 1 && options.PathPattern != "" && !strings.Contains(options.PathPattern, "{account}") {
				return fmt.Errorf("--path-pattern %q needs {account} to keep several subscriptions apart", options.PathPattern)
			}
			// A subscription that can't be imported doesn't stop the others.
			return importEach(options, "subscription", subscriptions, func(options ImportOptions, subscription string) error {
				return Import(newAzureProvider(), options, []string{options.ResourceGroup, subscription})
			})
		},
	}

	cmd.AddCommand(listCmd(newAzureProvider()))
	baseProviderFlags(cmd.PersistentFlags(), &options, "resource_group", "resource_group=name1:name2:name3")
	parallelFlag(cmd, &options, "subscriptions of --subscriptions or --management-group")
	cmd.PersistentFlags().StringVarP(&options.ResourceGroup, "resource-group", "R", "", "")
	cmd.PersistentFlags().StringSliceVar(&subscriptions, "subscriptions", nil, "subscription IDs to import, each into roots of its own (default ARM_SUBSCRIPTION_ID)")
	cmd.PersistentFlags().StringVar(&managementGroup, "management-group", "", "import every enabled subscription under this management group ID, in its child groups too (needs Reader on the group)")
	return cmd
}

func newAzureProvider() terraformutils.ProviderGenerator {
	return &azure_terraforming.AzureProvider{}
}

func init() {
	registerProvider("azure", newCmdAzureImporter, newAzureProvider)
}

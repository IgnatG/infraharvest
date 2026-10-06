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

//go:build !minimal || google

package cmd

import (
	"fmt"
	"log"
	"strings"

	gcp_terraforming "github.com/IgnatG/infraharvest/providers/gcp"
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/spf13/cobra"
)

func newCmdGoogleImporter(options ImportOptions) *cobra.Command {
	providerType := ""
	cmd := &cobra.Command{
		Use:   "google",
		Short: "Import current state to Terraform configuration from Google Cloud",
		Long:  "Import current state to Terraform configuration from Google Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			originalPathPattern := options.PathPattern
			// Each project gets roots of its own: without {account}, they
			// would write over each other's.
			if len(options.Projects) > 1 && originalPathPattern != "" && !strings.Contains(originalPathPattern, "{account}") && !strings.Contains(originalPathPattern, "{provider}/{service}") {
				return fmt.Errorf("--path-pattern %q needs {account} to keep several projects apart", originalPathPattern)
			}
			// A project that can't be imported doesn't stop the others.
			return importEach(options, "project", options.Projects, func(options ImportOptions, project string) error {
				for _, region := range options.Regions {
					provider := newGoogleProvider()
					options.PathPattern = strings.ReplaceAll(originalPathPattern, "{provider}/{service}", "{provider}/"+project+"/{service}/"+region)
					log.Println(provider.GetName() + " importing project " + project + " region " + region)
					if err := Import(provider, options, []string{region, project, providerType}); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	parallelFlag(cmd, &options, "projects of --projects")
	cmd.AddCommand(listCmd(newGoogleProvider()))
	baseProviderFlags(cmd.PersistentFlags(), &options, "firewalls,networks", "compute_firewall=id1:id2:id4")
	cmd.PersistentFlags().StringSliceVarP(&options.Regions, "regions", "z", []string{"global"}, "europe-west1,")
	cmd.PersistentFlags().StringSliceVarP(&options.Projects, "projects", "", []string{}, "")
	cmd.PersistentFlags().StringVarP(&providerType, "provider-type", "", "", "beta")
	_ = cmd.MarkPersistentFlagRequired("projects")
	return cmd
}

func newGoogleProvider() terraformutils.ProviderGenerator {
	return &gcp_terraforming.GCPProvider{}
}

func init() {
	registerProvider("google", newCmdGoogleImporter, newGoogleProvider)
}

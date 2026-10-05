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

package cmd

import (
	"github.com/spf13/cobra"
)

func NewCmdRoot() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "infraharvest",
		Short:         "Import existing infrastructure into Terraform code",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	cmd.AddCommand(newImportCmd())
	cmd.AddCommand(newDiscoverCmd())
	cmd.AddCommand(newPickCmd())
	cmd.AddCommand(newMCPCmd())
	cmd.AddCommand(newVerifyCmd())
	cmd.AddCommand(newReportCmd())
	cmd.AddCommand(newBootstrapCmd())
	cmd.AddCommand(versionCmd)
	return cmd
}

func Execute() error {
	cmd := NewCmdRoot()
	return cmd.Execute()
}

func providerImporterSubcommands() []func(options ImportOptions) *cobra.Command {
	var commands []func(options ImportOptions) *cobra.Command
	for _, name := range registeredProviders() {
		commands = append(commands, providerRegistry[name].newCmd)
	}
	return commands
}

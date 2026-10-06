//go:build !minimal || myrasec

package cmd

import (
	myrasec_terraforming "github.com/IgnatG/infraharvest/providers/myrasec"
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/spf13/cobra"
)

// newCmdMyrasecImporter
func newCmdMyrasecImporter(options ImportOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "myrasec",
		Short: "Import current state to Terraform configuration from Myra Security",
		Long:  "Import current state to Terraform configuration from Myra Security",
		RunE: func(_ *cobra.Command, args []string) error {
			provider := newMyrasecProvider()
			err := Import(provider, options, []string{})
			if err != nil {
				return err
			}
			return nil
		},
	}

	cmd.AddCommand(listCmd(newMyrasecProvider()))
	baseProviderFlags(cmd.PersistentFlags(), &options, "domain", "")
	return cmd
}

// newMyrasecProvider
func newMyrasecProvider() terraformutils.ProviderGenerator {
	return &myrasec_terraforming.Provider{}
}

func init() {
	registerProvider("myrasec", newCmdMyrasecImporter, newMyrasecProvider)
}

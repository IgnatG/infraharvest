package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is infraharvest's version. Release builds set it with
// -ldflags "-X github.com/IgnatG/infraharvest/cmd.version=v1.2.3"; go
// install records the module version, which init picks up.
var version = devVersion

// devVersion is the version of builds that set none.
const devVersion = "v0.1.0-dev"

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		version = buildVersion(version, info.Main.Version)
	}
}

// buildVersion returns the version go install recorded, if any and if the
// build didn't set one.
func buildVersion(set, recorded string) string {
	if set != devVersion || recorded == "" || recorded == "(devel)" {
		return set
	}
	return recorded
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of infraharvest",
	Run: func(_ *cobra.Command, args []string) {
		fmt.Println("infraharvest " + version)
	},
}

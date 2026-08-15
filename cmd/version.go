package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridable at build time via
//
//	go build -ldflags "-X github.com/ariefsam/esb/cmd.version=v1.2.3"
//
// When left empty (e.g. `go install ...@latest`), it falls back to the
// module version embedded by the Go toolchain in the build info.
var version = ""

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the esb version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("esb", resolveVersion())
	},
}

// resolveVersion returns the ldflags-injected version if set, otherwise the
// version recorded in the module build info, otherwise "(devel)".
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
